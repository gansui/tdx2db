package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/database"
)

// checkGapWindowDays 检查窗口长度：最近多少个交易日内的日线完整性。
// 覆盖 cn 假期合并导致的长休市（中秋+国庆等），15 个交易日约三周。
const checkGapWindowDays = 15

// maxGapPrinted 每个类别的告警明细最多打印条数，防止刷屏。
const maxGapPrinted = 50

var TaskCheckGap *Task

func init() {
	TaskCheckGap = &Task{
		Name:      "check_gap",
		DependsOn: []string{"update_daily", "update_symbol_names"}, // 代码名称先就绪，缺失明细才能带中文名
		SkipIf:    skipIfPlan(func(p *WorkPlan) bool { return !p.NeedDaily }),
		Executor:  executeCheckGap,
	}
	registerTask(TaskCheckGap, "update")
}

type gapItem struct {
	Symbol string
	Name   string    // 中文名，来自 raw_symbol_name；空时仅显示代码
	Date   time.Time // 缺失段起始交易日
	End    time.Time // 缺失段结束交易日
}

// label 代码 + 括号中文名（无中文名时仅代码）。
func (g gapItem) label() string {
	if g.Name == "" {
		return g.Symbol
	}
	return g.Symbol + " (" + g.Name + ")"
}

// dateRange 缺失日期段，单日无 "~"。
func (g gapItem) dateRange() string {
	dateStr := g.Date.Format("2006-01-02")
	if g.End.Equal(g.Date) {
		return dateStr
	}
	return dateStr + " ~ " + g.End.Format("2006-01-02")
}

type gapReport struct {
	midGaps    []gapItem // 断档：前后交易日均有数据、唯独该日缺失（疑似导入丢失）
	tailMisses []gapItem // 尾部缺失：最新交易日无数据（可能停牌/新股/当日未导入）
}

// executeCheckGap 检查最近若干交易日各股票日线是否连续完整，
// 缺口仅打印告警，不阻断后续 calc_basic/calc_factor。
// 窗口天数来自 args.GapDays（0 时用默认 checkGapWindowDays）。
func executeCheckGap(ctx context.Context, db database.DataRepository, args *TaskArgs) (*TaskResult, error) {
	windowDays := args.GapDays
	if windowDays <= 0 {
		windowDays = checkGapWindowDays
	}

	cal := (*TradingCalendar)(nil)
	if args.Plan != nil {
		cal = args.Plan.Calendar
	}
	if cal == nil {
		holidays, err := db.GetHolidays()
		if err != nil {
			return nil, fmt.Errorf("failed to load holidays: %w", err)
		}
		cal = NewTradingCalendar(holidays)
	}

	lastTrading := cal.LastTradingDayOnOrBefore(args.Today)

	// 向前推 N-1 个交易日得到窗口起点
	winStart := lastTrading
	for i := 0; i < windowDays-1; i++ {
		winStart = cal.LastTradingDayOnOrBefore(winStart.AddDate(0, 0, -1))
	}

	// 生成窗口内全部交易日
	tradingDays := make([]time.Time, 0, windowDays)
	for d := winStart; !d.After(lastTrading); d = nextTradingDay(cal, d) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		tradingDays = append(tradingDays, d)
	}

	rows, err := db.GetKlineDatesSince(winStart, "stock", "etf")
	if err != nil {
		return nil, fmt.Errorf("failed to query kline dates: %w", err)
	}

	// 按 symbol 建立存在的日期集合
	bySymbol := make(map[string]map[string]struct{})
	for _, r := range rows {
		set, ok := bySymbol[r.Symbol]
		if !ok {
			set = make(map[string]struct{})
			bySymbol[r.Symbol] = set
		}
		set[r.Date.Format("2006-01-02")] = struct{}{}
	}

	dayStrs := make([]string, len(tradingDays))
	for i, d := range tradingDays {
		dayStrs[i] = d.Format("2006-01-02")
	}

	var report gapReport
	for symbol, set := range bySymbol {
		// 按"连续缺失段"判定：段前有数据且段后有数据 → 断档；段一直延伸到最新交易日 → 尾部缺失
		lo := -1
		for i := 1; i < len(dayStrs); i++ {
			_, has := set[dayStrs[i]]
			if !has {
				if lo == -1 {
					lo = i
				}
			}

			// 段结束：遇到有数据的日子，或已到窗口末尾
			segmentEnds := lo != -1 && (has || i == len(dayStrs)-1)
			if !segmentEnds {
				continue
			}
			hi := i
			if has {
				hi = i - 1 // 不含当前有数据的日子
			}

			item := gapItem{Symbol: symbol, Date: tradingDays[lo], End: tradingDays[hi]}
			// 段前无数据：上市起点/窗口开头附近，不判缺失
			hasBefore := false
			for j := lo - 1; j >= 0; j-- {
				if _, ok := set[dayStrs[j]]; ok {
					hasBefore = true
					break
				}
			}
			if !hasBefore {
				lo = -1
				continue
			}
			// 段后还有数据：中间断档，高置信数据丢失
			hasAfter := false
			for j := hi + 1; j < len(dayStrs); j++ {
				if _, ok := set[dayStrs[j]]; ok {
					hasAfter = true
					break
				}
			}
			if hasAfter {
				report.midGaps = append(report.midGaps, item)
			} else if hi == len(dayStrs)-1 {
				report.tailMisses = append(report.tailMisses, item)
			}
			lo = -1
		}
	}

	sortGapItems(report.midGaps)
	sortGapItems(report.tailMisses)

	fillGapSymbolNames(db, &report)

	return buildCheckGapResult(&report, lastTrading, windowDays)
}

// fillGapSymbolNames 去重收集涉及缺失的代码，从 raw_symbol_name 补齐中文名；
// 名称表为空/未拉取时静默跳过，不影响缺失检查结果。
func fillGapSymbolNames(db database.DataRepository, report *gapReport) {
	if len(report.midGaps)+len(report.tailMisses) == 0 {
		return
	}
	seen := make(map[string]struct{})
	codes := make([]string, 0, len(report.midGaps)+len(report.tailMisses))
	for _, g := range report.midGaps {
		if _, ok := seen[g.Symbol]; !ok {
			seen[g.Symbol] = struct{}{}
			codes = append(codes, g.Symbol)
		}
	}
	for _, g := range report.tailMisses {
		if _, ok := seen[g.Symbol]; !ok {
			seen[g.Symbol] = struct{}{}
			codes = append(codes, g.Symbol)
		}
	}

	names, err := db.GetSymbolNamesByCode(codes)
	if err != nil {
		fmt.Printf("  ⚠️ 无法读取代码中文名（不影响检查结果）: %v\n", err)
		return
	}
	for i := range report.midGaps {
		report.midGaps[i].Name = names[report.midGaps[i].Symbol]
	}
	for i := range report.tailMisses {
		report.tailMisses[i].Name = names[report.tailMisses[i].Symbol]
	}
}

// nextTradingDay 返回 d 之后的下一个交易日。
func nextTradingDay(cal *TradingCalendar, d time.Time) time.Time {
	for cur := d.AddDate(0, 0, 1); ; cur = cur.AddDate(0, 0, 1) {
		if cal.IsTradingDay(cur) {
			return cur
		}
	}
}

func sortGapItems(items []gapItem) {
	sort.Slice(items, func(a, b int) bool {
		if items[a].Symbol != items[b].Symbol {
			return items[a].Symbol < items[b].Symbol
		}
		return items[a].Date.Before(items[b].Date)
	})
}

func buildCheckGapResult(report *gapReport, lastTrading time.Time, windowDays int) (*TaskResult, error) {
	window := fmt.Sprintf("最近 %d 个交易日", windowDays)
	if len(report.midGaps) == 0 && len(report.tailMisses) == 0 {
		fmt.Printf("✅ %s日线完整（截至 %s），无缺失\n",
			window, lastTrading.Format("2006-01-02"))
		return &TaskResult{State: StateCompleted, Message: "no gaps"}, nil
	}

	fmt.Printf("⚠️ %s日线存在缺失（截至 %s）：\n",
		window, lastTrading.Format("2006-01-02"))

	total := 0
	if len(report.midGaps) > 0 {
		total += len(report.midGaps)
		fmt.Printf("  🚨 断档 %d 条（前后均有数据，疑似导入丢失）：\n", len(report.midGaps))
		printGapItems(report.midGaps)
	}
	if len(report.tailMisses) > 0 {
		total += len(report.tailMisses)
		fmt.Printf("  ⚠️  最新交易日 (%s) 缺失 %d 条（可能停牌/新股，可忽略）：\n",
			lastTrading.Format("2006-01-02"), len(report.tailMisses))
		printGapItems(report.tailMisses)
	}

	return &TaskResult{
		State:   StateCompleted, // 仅告警，不阻断流程
		Rows:    total,
		Message: fmt.Sprintf("%d gaps detected", total),
	}, nil
}

func printGapItems(items []gapItem) {
	limit := len(items)
	if limit > maxGapPrinted {
		limit = maxGapPrinted
	}
	// 预计算「代码 (中文名)」列显示宽度，不足的统一补空格，让缺失日期列对齐。
	labels := make([]string, len(items))
	maxW := 0
	for i := range items {
		labels[i] = items[i].label()
		if w := dispWidth(labels[i]); w > maxW {
			maxW = w
		}
	}
	for i := 0; i < limit; i++ {
		pad := strings.Repeat(" ", maxW-dispWidth(labels[i]))
		fmt.Printf("     %s%s  缺失 %s\n", labels[i], pad, items[i].dateRange())
	}
	if len(items) > maxGapPrinted {
		fmt.Printf("     … 其余 %d 条略\n", len(items)-maxGapPrinted)
	}
}

// dispWidth 显示宽度：全角（中文/全角标点）按 2 格、半角按 1 格，用于等宽对齐。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		if r >= 0x2e80 && r <= 0x9fff || r >= 0xf900 && r <= 0xfaff ||
			r >= 0xff00 && r <= 0xff60 || r >= 0x3000 && r <= 0x303f {
			w += 2
		} else {
			w++
		}
	}
	return w
}

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jing2uo/tdx2db/cmd"
	"github.com/spf13/cobra"
)

// 由 ldflags 注入（见 .github/workflows/release.yaml）；
// 未注入时（裸 go build）从 runtime/debug 读 VCS 信息。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func buildVersionString() string {
	v, c, d := version, commit, date
	var dirty bool
	if v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					c = s.Value
				case "vcs.time":
					d = s.Value
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
		}
	}
	if len(c) > 7 {
		c = c[:7]
	}
	if dirty {
		c += " (dirty)"
	}
	return fmt.Sprintf("tdx2db %s\ncommit: %s\nbuilt:  %s", v, c, d)
}

const dbURIInfo = "数据库连接信息"
const dbURIHelp = `

Database URI:
  ClickHouse: clickhouse://[user[:password]@][host][:port][/database][?http_port=p&]
  DuckDB:     duckdb://[path]`

const dayFileInfo = "通达信日线文件目录"
const minInfo = "导入 1 分钟分时数据（可选）"
const gapDaysInfo = "日线完整性检查窗口（交易日数），0 表示默认 15"
const dateInfo = "手动补齐指定日期（YYYY-MM-DD），仅下载该一天的日线增量"
const statsDaysInfo = "回溯统计的交易日数量（默认 15）"

// 启用 --log 后，默认
const logInfo = "把命令输出同步追加到当前目录 tdx2db-<日期>.log（同名文件追加，一天一个）"

// 日志镜像相关全局状态（仅 --log 启用时使用）。
var (
	logFile    *os.File
	logRealOut *os.File
	logRealErr *os.File
	logPipeW   *os.File
	logDone    chan struct{}
)

// enableLog 把 stdout/stderr 实时镜像写入当前目录 tdx2db-YYYYMMDD.log（追加模式）。
// 通过 pipe + io.Copy 双写到"屏幕 + 日志文件"，保证一天内多次执行共用同一文件。
func enableLog() error {
	name := fmt.Sprintf("tdx2db-%s.log", time.Now().Format("20060102"))
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("创建日志文件 %s 失败: %w", name, err)
	}
	logFile = f
	logRealOut = os.Stdout
	logRealErr = os.Stderr

	r, w, err := os.Pipe()
	if err != nil {
		f.Close()
		return fmt.Errorf("创建日志管道失败: %w", err)
	}
	logPipeW = w
	logDone = make(chan struct{})

	os.Stdout = w
	os.Stderr = w

	go func() {
		defer r.Close()
		io.Copy(io.MultiWriter(logRealOut, f), r)
		close(logDone)
	}()

	fmt.Printf("🗒️  日志写入: %s\n", name)
	return nil
}

// finishLog 还原屏幕输出并关闭日志文件，等拷贝 goroutine 收尾。
func finishLog() {
	if logFile == nil {
		return
	}
	os.Stdout = logRealOut
	os.Stderr = logRealErr
	if logPipeW != nil {
		logPipeW.Close()
	}
	if logDone != nil {
		<-logDone
	}
	logFile.Close()
	logFile = nil
	logDone = nil
	logPipeW = nil
}

func main() {
	// 创建可取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		fmt.Printf("\n🚨 收到信号 %v，正在退出...\n", sig)
		cancel()
	}()

	versionStr := buildVersionString()
	var tempDirOverride string
	var logEnable bool
	var rootCmd = &cobra.Command{
		Use:           "tdx2db",
		Short:         "Load TDX Data to DuckDB",
		SilenceErrors: true,
		Version:       versionStr,
		PersistentPreRunE: func(c *cobra.Command, args []string) error {
			if err := cmd.OverrideTempDir(tempDirOverride); err != nil {
				return err
			}
			if logEnable {
				return enableLog()
			}
			return nil
		},
	}
	// 预注册 -v 短选项；cobra 默认只挂 --version
	rootCmd.Flags().BoolP("version", "v", false, "version for tdx2db")
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	// --temp: 把临时目录的父目录从默认 $TMPDIR 切到指定路径,
	// 适用 $TMPDIR (常见 /tmp tmpfs) 容量被占满时的兜底。
	rootCmd.PersistentFlags().StringVar(&tempDirOverride, "temp", "",
		"临时文件父目录, 留空走 $TMPDIR")
	rootCmd.PersistentFlags().BoolVar(&logEnable, "log", false, logInfo)

	var versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(c *cobra.Command, args []string) {
			fmt.Println(versionStr)
		},
	}

	var (
		dbURI      string
		dayFileDir string
		minEnable  bool
		gapDays    int
		dateStr    string
		statsDays  int
	)

	var initCmd = &cobra.Command{
		Use:   "init",
		Short: "Fully import stocks data from TDX",
		Example: `  tdx2db init --dburi 'clickhouse://localhost' --dayfiledir /path/to/vipdoc/
  tdx2db init --dburi 'duckdb://./tdx.db' --dayfiledir /path/to/vipdoc/` + dbURIHelp,
		RunE: func(c *cobra.Command, args []string) error {
			return cmd.Init(ctx, dbURI, dayFileDir)
		},
	}

	var cronCmd = &cobra.Command{
		Use:   "cron",
		Short: "Cron for update data and calc factor",
		Example: `  tdx2db cron --dburi 'clickhouse://localhost' --min
  tdx2db cron --dburi 'duckdb://./tdx.db'` + dbURIHelp,
		RunE: func(c *cobra.Command, args []string) error {
			var downloadDate time.Time
			if dateStr != "" {
				parsed, err := time.ParseInLocation("2006-01-02", dateStr, time.Local)
				if err != nil {
					return fmt.Errorf("无效的 --date %q（需 YYYY-MM-DD）: %w", dateStr, err)
				}
				downloadDate = parsed
			}
			return cmd.Cron(ctx, dbURI, minEnable, gapDays, downloadDate)
		},
	}

	var statsCmd = &cobra.Command{
		Use:   "stats",
		Short: "统计最近 N 个交易日逐日入库条数，识别原始数据缺失",
		Example: `  tdx2db stats --dburi 'duckdb://./tdx.db'
  tdx2db stats --dburi 'duckdb://./tdx.db' --days 30` + dbURIHelp,
		RunE: func(c *cobra.Command, args []string) error {
			return cmd.Stats(ctx, dbURI, statsDays)
		},
	}

	// Init Flags
	initCmd.Flags().StringVar(&dbURI, "dburi", "", dbURIInfo)
	initCmd.Flags().StringVar(&dayFileDir, "dayfiledir", "", dayFileInfo)
	initCmd.MarkFlagRequired("dburi")
	initCmd.MarkFlagRequired("dayfiledir")

	// Cron Flags
	cronCmd.Flags().StringVar(&dbURI, "dburi", "", dbURIInfo)
	cronCmd.MarkFlagRequired("dburi")
	cronCmd.Flags().BoolVar(&minEnable, "min", false, minInfo)
	cronCmd.Flags().IntVar(&gapDays, "gap-days", 0, gapDaysInfo)
	cronCmd.Flags().StringVar(&dateStr, "date", "", dateInfo)

	// Stats Flags
	statsCmd.Flags().StringVar(&dbURI, "dburi", "", dbURIInfo)
	statsCmd.MarkFlagRequired("dburi")
	statsCmd.Flags().IntVar(&statsDays, "days", 15, statsDaysInfo)

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(cronCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(statsCmd)

	cobra.OnFinalize(func() {
		os.RemoveAll(cmd.TempDir)
		finishLog()
	})

	if err := rootCmd.Execute(); err != nil {
		if err == context.Canceled {
			fmt.Fprintln(os.Stderr, "✅ 任务安全中断")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "🛑 错误: %v\n", err)
		os.Exit(1)
	}
}

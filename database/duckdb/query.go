package duckdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jing2uo/tdx2db/model"
)

func (d *DuckDBDriver) Query(table string, conditions map[string]interface{}, dest interface{}) error {
	query := fmt.Sprintf("SELECT * FROM %s", table)
	args := []interface{}{}
	if len(conditions) > 0 {
		whereParts := []string{}
		i := 1
		for k, v := range conditions {
			whereParts = append(whereParts, fmt.Sprintf("%s = $%d", k, i))
			args = append(args, v)
			i++
		}
		query += " WHERE " + strings.Join(whereParts, " AND ")
	}

	return d.db.Select(dest, query, args...)
}

func (d *DuckDBDriver) GetLatestDate(tableName string, dateCol string) (time.Time, error) {
	query := fmt.Sprintf("SELECT DATE(max(%s)) AS latest FROM %s", dateCol, tableName)

	var latest sql.NullTime
	err := d.db.Get(&latest, query)
	if err != nil {
		return time.Time{}, err
	}

	if !latest.Valid {
		return time.Time{}, nil
	}

	return latest.Time, nil
}

func (d *DuckDBDriver) GetMinDate(tableName string, dateCol string) (time.Time, error) {
	query := fmt.Sprintf("SELECT DATE(min(%s)) AS earliest FROM %s", dateCol, tableName)

	var earliest sql.NullTime
	err := d.db.Get(&earliest, query)
	if err != nil {
		return time.Time{}, err
	}

	if !earliest.Valid {
		return time.Time{}, nil
	}

	return earliest.Time, nil
}

func (d *DuckDBDriver) GetSymbolsByClass(classes ...string) ([]string, error) {
	if len(classes) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(classes))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(
		"SELECT symbol FROM %s WHERE class IN (%s) ORDER BY symbol",
		model.TableSymbolClass.TableName, placeholders,
	)
	args := make([]any, len(classes))
	for i, c := range classes {
		args[i] = c
	}

	var symbols []string
	if err := d.db.Select(&symbols, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query symbols by class: %w", err)
	}
	return symbols, nil
}

func (d *DuckDBDriver) CountKlineDaily() (int64, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", model.TableKlineDaily.TableName)

	var count int64
	err := d.db.Get(&count, query)
	if err != nil {
		return 0, fmt.Errorf("failed to count kline daily: %w", err)
	}

	return count, nil
}

func (d *DuckDBDriver) QueryKlineDaily(symbol string, startDate, endDate *time.Time) ([]model.KlineDay, error) {
	conditions := []string{"symbol = ?"}
	args := []interface{}{symbol}

	if startDate != nil {
		conditions = append(conditions, "date >= ?")
		args = append(args, *startDate)
	}
	if endDate != nil {
		conditions = append(conditions, "date <= ?")
		args = append(args, *endDate)
	}

	query := fmt.Sprintf(
		`SELECT * FROM %s WHERE %s ORDER BY date ASC`,
		model.TableKlineDaily.TableName,
		strings.Join(conditions, " AND "),
	)

	var results []model.KlineDay
	if err := d.db.Select(&results, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query kline daily: %w", err)
	}

	return results, nil
}

func (d *DuckDBDriver) GetBasicsBySymbol(symbol string) ([]model.BasicDaily, error) {
	query := fmt.Sprintf(
		"SELECT * FROM %s WHERE symbol = ? ORDER BY date",
		model.TableBasicDaily.TableName,
	)

	var results []model.BasicDaily
	if err := d.db.Select(&results, query, symbol); err != nil {
		return nil, fmt.Errorf("failed to query daily basics by symbol %s: %w", symbol, err)
	}

	return results, nil
}

func (d *DuckDBDriver) GetGbbq() ([]model.GbbqData, error) {
	table := model.TableGbbq.TableName

	query := fmt.Sprintf(`SELECT * FROM %s ORDER BY symbol, date`, table)

	var results []model.GbbqData
	if err := d.db.Select(&results, query); err != nil {
		return nil, fmt.Errorf("failed to query gbbq: %w", err)
	}

	return results, nil
}

func (d *DuckDBDriver) GetHolidays() ([]time.Time, error) {
	query := fmt.Sprintf("SELECT date FROM %s ORDER BY date", model.TableHoliday.TableName)
	var dates []time.Time
	if err := d.db.Select(&dates, query); err != nil {
		return nil, fmt.Errorf("failed to query holidays: %w", err)
	}
	return dates, nil
}

func (d *DuckDBDriver) GetKlineDatesSince(since time.Time, classes ...string) ([]model.KlineDateRow, error) {
	placeholders := strings.Repeat("?,", len(classes))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(
		`SELECT k.symbol AS symbol, k.date AS date
		 FROM %s k
		 WHERE k.date >= ?
		   AND k.symbol IN (SELECT symbol FROM %s WHERE class IN (%s))
		 ORDER BY k.symbol, k.date`,
		model.TableKlineDaily.TableName,
		model.TableSymbolClass.TableName,
		placeholders,
	)

	args := make([]any, 0, len(classes)+1)
	args = append(args, since)
	for _, c := range classes {
		args = append(args, c)
	}

	rows := make([]model.KlineDateRow, 0)
	if err := d.db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query kline dates: %w", err)
	}
	return rows, nil
}

func (d *DuckDBDriver) GetSymbolNamesByCode(codes []string) (map[string]string, error) {
	res := map[string]string{}
	if len(codes) == 0 {
		return res, nil
	}
	placeholders := strings.Repeat("?,", len(codes))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(
		"SELECT symbol, name FROM %s WHERE symbol IN (%s)",
		model.TableSymbolName.TableName,
		placeholders,
	)
	args := make([]any, len(codes))
	for i, c := range codes {
		args[i] = c
	}

	var rows []model.KlineSymbolName
	if err := d.db.Select(&rows, query, args...); err != nil {
		return nil, fmt.Errorf("failed to query symbol names: %w", err)
	}
	for _, r := range rows {
		res[r.Symbol] = r.Name
	}
	return res, nil
}

func (d *DuckDBDriver) GetLatestKlineDate() (model.KlineLatestDate, error) {
	query := fmt.Sprintf(
		"SELECT DATE(max(date)) AS latest, count(*) AS cnt FROM %s",
		model.TableKlineDaily.TableName,
	)
	var res model.KlineLatestDate
	if err := d.db.Get(&res, query); err != nil {
		return res, fmt.Errorf("failed to query latest kline date: %w", err)
	}
	return res, nil
}

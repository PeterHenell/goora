package main

import (
	"database/sql"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-oci8"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
)

var (
	// Version will be set at build time.
	Version       = "0.0.0.dev"
	listenAddress = flag.String("web.listen-address", ":9161", "Address to listen on for web interface and telemetry.")
	metricPath    = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	landingPage   = []byte("<html><head><title>Oracle DB Exporter " + Version + "</title></head><body><h1>Oracle DB Exporter " + Version + "</h1><p><a href='" + *metricPath + "'>Metrics</a></p></body></html>")
)

// Metric name parts.
const (
	namespace = "oracledb"
	exporter  = "exporter"
)

// Exporter collects Oracle DB metrics. It implements prometheus.Collector.
type Exporter struct {
	dsn             string
	duration, error prometheus.Gauge
	totalScrapes    prometheus.Counter
	scrapeErrors    *prometheus.CounterVec
	up              prometheus.Gauge
}

// NewExporter returns a new Oracle DB exporter for the provided DSN.
func NewExporter(dsn string) *Exporter {
	return &Exporter{
		dsn: dsn,
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_duration_seconds",
			Help:      "Duration of the last scrape of metrics from Oracle DB.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrapes_total",
			Help:      "Total number of times Oracle DB was scraped for metrics.",
		}),
		scrapeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrape_errors_total",
			Help:      "Total number of times an error occured scraping a Oracle database.",
		}, []string{"collector"}),
		error: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_error",
			Help:      "Whether the last scrape of metrics from Oracle DB resulted in an error (1 for error, 0 for success).",
		}),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Whether the Oracle database server is up.",
		}),
	}
}

// Describe describes all the metrics exported by the MS SQL exporter.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	// We cannot know in advance what metrics the exporter will generate
	// So we use the poor man's describe method: Run a collect
	// and send the descriptors of all the collected metrics. The problem
	// here is that we need to connect to the Oracle DB. If it is currently
	// unavailable, the descriptors will be incomplete. Since this is a
	// stand-alone exporter and not used as a library within other code
	// implementing additional metrics, the worst that can happen is that we
	// don't detect inconsistent metrics created by this exporter
	// itself. Also, a change in the monitored Oracle instance may change the
	// exported metrics during the runtime of the exporter.

	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh

}

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.error
	e.scrapeErrors.Collect(ch)
	ch <- e.up
}

func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	e.totalScrapes.Inc()
	var err error
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
		if err == nil {
			e.error.Set(0)
		} else {
			e.error.Set(1)
		}
	}(time.Now())

	db, err := sql.Open("oci8", e.dsn)
	if err != nil {
		log.Errorln("Error opening connection to database:", err)
		return
	}
	defer db.Close()

	isUpRows, err := db.Query("SELECT 1 FROM DUAL")
	if err != nil {
		log.Errorln("Error pinging oracle:", err)
		e.up.Set(0)
		return
	}
	isUpRows.Close()
	e.up.Set(1)

	if err = ScrapeActivity(db, ch); err != nil {
		log.Errorln("Error scraping for activity:", err)
		e.scrapeErrors.WithLabelValues("activity").Inc()
	}

	if err = ScrapeTablespace(db, ch); err != nil {
		log.Errorln("Error scraping for tablespace:", err)
		e.scrapeErrors.WithLabelValues("tablespace").Inc()
	}

	if err = ScrapeWaitTime(db, ch); err != nil {
		log.Errorln("Error scraping for wait_time:", err)
		e.scrapeErrors.WithLabelValues("wait_time").Inc()
	}

	if err = ScrapeSessions(db, ch); err != nil {
		log.Errorln("Error scraping for sessions:", err)
		e.scrapeErrors.WithLabelValues("sessions").Inc()
	}

	if err = ScrapeOsStats(db, ch); err != nil {
		log.Errorln("Error scraping for osstats:", err)
		e.scrapeErrors.WithLabelValues("osstats").Inc()
	}

	if err = ScrapeFRA(db, ch); err != nil {
		log.Errorln("Error scraping for fra:", err)
		e.scrapeErrors.WithLabelValues("fra").Inc()
	}

	if err = ScrapeUsers(db, ch); err != nil {
		log.Errorln("Error scraping for users:", err)
		e.scrapeErrors.WithLabelValues("users").Inc()
	}

	if err = ScrapeRMAN(db, ch); err != nil {
		log.Errorln("Error scraping for rman:", err)
		e.scrapeErrors.WithLabelValues("rman").Inc()
	}

	if err = ScrapeInstanceStatus(db, ch); err != nil {
		log.Errorln("Error scraping for status:", err)
		e.scrapeErrors.WithLabelValues("status").Inc()
	}

	if err = ScrapeSqlStats(db, ch); err != nil {
		log.Errorln("Error scraping for sql stats:", err)
		e.scrapeErrors.WithLabelValues("sqlstats").Inc()
	}

}

func ScrapeUsers(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	// Retrieve status and expiry date for all accounts.
	rows, err = db.Query("SELECT TRUNC(expiry_date) - TRUNC(sysdate) as expiration, username, REPLACE(account_status, ' ', '') FROM dba_users")
	if err != nil {
		return err
	}

	defer rows.Close()

	for rows.Next() {
		var (
			expiration     float64
			username       string
			account_status string
		)
		if err := rows.Scan(&expiration, &username, &account_status); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "users", "expiration"),
				"Utilisateurs", []string{"username", "status"}, nil),
			prometheus.CounterValue,
			expiration,
			username,
			account_status,
		)
	}
	return nil
}

// scrapes values from v$sqlstats
func ScrapeSqlStats(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	// Retrieve status and expiry date for all accounts.
	rows, err = db.Query("select SQL_ID, parse_calls, disk_reads, direct_writes, direct_reads, buffer_gets, rows_processed, fetches, executions, loads, invalidations, cpu_time, elapsed_time, avg_hard_parse_time, application_wait_time, concurrency_wait_time, user_IO_wait_time, plsql_exec_time, java_exec_time, sorts, sharable_mem, total_sharable_mem, physical_read_bytes, physical_read_requests, physical_write_bytes, physical_write_requests  from v$sqlstats")
	if err != nil {
		return err
	}

	defer rows.Close()

	for rows.Next() {
		var (
			SQL_ID                  string
			parse_calls             float64
			disk_reads              float64
			direct_writes           float64
			direct_reads            float64
			buffer_gets             float64
			rows_processed          float64
			fetches                 float64
			executions              float64
			loads                   float64
			invalidations           float64
			cpu_time                float64
			elapsed_time            float64
			avg_hard_parse_time     float64
			application_wait_time   float64
			concurrency_wait_time   float64
			user_IO_wait_time       float64
			plsql_exec_time         float64
			java_exec_time          float64
			sorts                   float64
			sharable_mem            float64
			total_sharable_mem      float64
			physical_read_bytes     float64
			physical_read_requests  float64
			physical_write_bytes    float64
			physical_write_requests float64
		)
		if err := rows.Scan(&SQL_ID, &parse_calls, &disk_reads, &direct_writes, &direct_reads, &buffer_gets, &rows_processed, &fetches, &executions, &loads, &invalidations, &cpu_time, &elapsed_time, &avg_hard_parse_time, &application_wait_time, &concurrency_wait_time, &user_IO_wait_time, &plsql_exec_time, &java_exec_time, &sorts, &sharable_mem, &total_sharable_mem, &physical_read_bytes, &physical_read_requests, &physical_write_bytes, &physical_write_requests); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "parse_calls"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			parse_calls,
			SQL_ID,
			"parse_calls",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "disk_reads"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			disk_reads,
			SQL_ID,
			"disk_reads",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "direct_writes"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			direct_writes,
			SQL_ID,
			"direct_writes",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "direct_reads"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			direct_reads,
			SQL_ID,
			"direct_reads",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "buffer_gets"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			buffer_gets,
			SQL_ID,
			"buffer_gets",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "rows_processed"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			rows_processed,
			SQL_ID,
			"rows_processed",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "fetches"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			fetches,
			SQL_ID,
			"fetches",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "executions"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			executions,
			SQL_ID,
			"executions",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "loads"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			loads,
			SQL_ID,
			"loads",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "invalidations"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			invalidations,
			SQL_ID,
			"invalidations",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "cpu_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			cpu_time,
			SQL_ID,
			"cpu_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "elapsed_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			elapsed_time,
			SQL_ID,
			"elapsed_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "avg_hard_parse_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			avg_hard_parse_time,
			SQL_ID,
			"avg_hard_parse_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "application_wait_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			application_wait_time,
			SQL_ID,
			"application_wait_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "plsql_exec_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			plsql_exec_time,
			SQL_ID,
			"plsql_exec_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "java_exec_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			java_exec_time,
			SQL_ID,
			"java_exec_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "user_IO_wait_time"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			user_IO_wait_time,
			SQL_ID,
			"user_IO_wait_time",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "sorts"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			sorts,
			SQL_ID,
			"sorts",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "sharable_mem"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			sharable_mem,
			SQL_ID,
			"sharable_mem",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "total_sharable_mem"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			total_sharable_mem,
			SQL_ID,
			"total_sharable_mem",
		)

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "physical_read_bytes"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			physical_read_bytes,
			SQL_ID,
			"physical_read_bytes",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "physical_read_requests"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			physical_read_requests,
			SQL_ID,
			"physical_read_requests",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "physical_write_bytes"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			physical_write_bytes,
			SQL_ID,
			"physical_write_bytes",
		)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sqlstats", "physical_write_requests"),
				"Information from v$sqlstats", []string{"sqlid","stat"}, nil),
			prometheus.GaugeValue,
			physical_write_requests,
			SQL_ID,
			"physical_write_requests",
		)

	}
	return nil
}

func ScrapeFRA(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)

	rows, err = db.Query("select trunc(sum(PERCENT_SPACE_USED)) as value from v$flash_recovery_area_usage")
	if err != nil {
		return err
	}

	defer rows.Close()

	for rows.Next() {
		var (
			value float64
		)
		if err := rows.Scan(&value); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "fra", "used"),
				"FRA disponible", []string{}, nil),
			prometheus.GaugeValue,
			value,
		)
	}
	return nil
}

func ScrapeRMAN(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	// Retrieve status and type for all sessions.
	rows, err = db.Query("SELECT row_level, operation AS bkp_operation, status AS bkp_status, object_type, TO_CHAR(start_time,'YYYY-MM-DD_HH24:MI:SS') AS bkp_start_time, TO_CHAR(end_time,'YYYY-MM-DD_HH24:MI:SS') as bkp_end_time, TO_CHAR(start_time,'DD') as bkp_start_nbday FROM V$RMAN_STATUS WHERE START_TIME > SYSDATE -1 AND operation = 'BACKUP'")
	if err != nil {
		return err
	}

	defer rows.Close()

	for rows.Next() {
		var (
			Value           float64
			row_level       float64
			bkp_operation   string
			bkp_status      string
			object_type     string
			bkp_start_time  string
			bkp_end_time    string
			bkp_start_nbday string
		)
		if err := rows.Scan(&row_level, &bkp_operation, &bkp_status, &object_type, &bkp_start_time, &bkp_end_time, &bkp_start_nbday); err != nil {
			return err
		}

		if bkp_status == "COMPLETED" {
			Value = 0 // Green value
		} else
		if bkp_status == "RUNNING" {
			Value = 1 // Yellow value
		} else {
			Value = 2 // Red value
		}

		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "rman", "backup"),
				"Backup RMAN", []string{"operation", "status", "type", "start_time", "end_time", "start_nbday"}, nil),
			prometheus.GaugeValue,
			Value,
			bkp_operation,
			bkp_status,
			object_type,
			bkp_start_time,
			bkp_end_time,
			bkp_start_nbday,
		)
	}
	return nil
}

// ScrapeInstanceStatus collects session metrics from the v$instance view.
func ScrapeInstanceStatus(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT instance_name as name, status from v$instance")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var name string
		var value float64
		if err := rows.Scan(&name, &status); err != nil {
			return err
		}
		if status == "OPEN" {
			value = 2
		} else
		if status == "MOUNTED" {
			value = 1
		} else {
			value = 0
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "status", name),
				"Statut de la base", []string{}, nil),
			prometheus.CounterValue,
			value,
		)
	}
	return nil
}

// ScrapeSessions collects session metrics from the v$session view.
func ScrapeSessions(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	// Retrieve status and type for all sessions.
	rows, err = db.Query("SELECT status, type, COUNT(*) FROM v$session GROUP BY status, type")
	if err != nil {
		return err
	}

	defer rows.Close()
	activeCount := 0.
	inactiveCount := 0.
	for rows.Next() {
		var (
			status      string
			sessionType string
			count       float64
		)
		if err := rows.Scan(&status, &sessionType, &count); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "sessions", "activity"),
				"Gauge metric with count of sessions by status and type", []string{"status", "type"}, nil),
			prometheus.GaugeValue,
			count,
			status,
			sessionType,
		)

		// These metrics are deprecated though so as to not break existing monitoring straight away, are included for the next few releases.
		if status == "ACTIVE" {
			activeCount += count
		}

		if status == "INACTIVE" {
			inactiveCount += count
		}
	}

	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(prometheus.BuildFQName(namespace, "sessions", "active"),
			"Gauge metric with count of sessions marked ACTIVE. DEPRECATED: use sum(oracledb_sessions_activity{status='ACTIVE}) instead.", []string{}, nil),
		prometheus.GaugeValue,
		activeCount,
	)
	ch <- prometheus.MustNewConstMetric(
		prometheus.NewDesc(prometheus.BuildFQName(namespace, "sessions", "inactive"),
			"Gauge metric with count of sessions marked INACTIVE. DEPRECATED: use sum(oracledb_sessions_activity{status='INACTIVE'}) instead.", []string{}, nil),
		prometheus.GaugeValue,
		inactiveCount,
	)
	return nil
}

// ScrapeWaitTime collects wait time metrics from the v$waitclassmetric view.
func ScrapeWaitTime(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT n.wait_class, round(m.time_waited/m.INTSIZE_CSEC,3) AAS from v$waitclassmetric  m, v$system_wait_class n where m.wait_class_id=n.wait_class_id and n.wait_class != 'Idle'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var value float64
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		name = cleanName(name)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "wait_time", name),
				"Generic counter metric from v$waitclassmetric view in Oracle.", []string{}, nil),
			prometheus.CounterValue,
			value,
		)
	}
	return nil
}

// ScrapeActivity collects activity metrics from the v$sysstat view.
func ScrapeActivity(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("SELECT name, value FROM v$sysstat WHERE name IN ('parse count (total)', 'execute count', 'user commits', 'user rollbacks')")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		var value float64
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		name = cleanName(name)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "activity", name),
				"Generic counter metric from v$sysstat view in Oracle.", []string{}, nil),
			prometheus.CounterValue,
			value,
		)
	}
	return nil
}

// ScrapeTablespace collects tablespace size.
func ScrapeTablespace(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query(`
SELECT
  Z.name,
  dt.status,
  dt.contents,
  dt.extent_management,
  Z.bytes,
  Z.max_bytes,
  Z.free_bytes
FROM
(
  SELECT
    X.name                   as name,
    SUM(nvl(X.free_bytes,0)) as free_bytes,
    SUM(X.bytes)             as bytes,
    SUM(X.max_bytes)         as max_bytes
  FROM
    (
      SELECT
        ddf.tablespace_name as name,
        ddf.status as status,
        ddf.bytes as bytes,
        sum(dfs.bytes) as free_bytes,
        CASE
          WHEN ddf.maxbytes = 0 THEN ddf.bytes
          ELSE ddf.maxbytes
        END as max_bytes
      FROM
        sys.dba_data_files ddf,
        sys.dba_tablespaces dt,
        sys.dba_free_space dfs
      WHERE ddf.tablespace_name = dt.tablespace_name
      AND ddf.file_id = dfs.file_id(+)
      GROUP BY
        ddf.tablespace_name,
        ddf.file_name,
        ddf.status,
        ddf.bytes,
        ddf.maxbytes
    ) X
  GROUP BY X.name
  UNION ALL
  SELECT
    Y.name                   as name,
    MAX(nvl(Y.free_bytes,0)) as free_bytes,
    SUM(Y.bytes)             as bytes,
    SUM(Y.max_bytes)         as max_bytes
  FROM
    (
      SELECT
        dtf.tablespace_name as name,
        dtf.status as status,
        dtf.bytes as bytes,
        (
          SELECT
            ((f.total_blocks - s.tot_used_blocks)*vp.value)
          FROM
            (SELECT tablespace_name, sum(used_blocks) tot_used_blocks FROM gv$sort_segment WHERE  tablespace_name!='DUMMY' GROUP BY tablespace_name) s,
            (SELECT tablespace_name, sum(blocks) total_blocks FROM dba_temp_files where tablespace_name !='DUMMY' GROUP BY tablespace_name) f,
            (SELECT value FROM v$parameter WHERE name = 'db_block_size') vp
          WHERE f.tablespace_name=s.tablespace_name AND f.tablespace_name = dtf.tablespace_name
        ) as free_bytes,
        CASE
          WHEN dtf.maxbytes = 0 THEN dtf.bytes
          ELSE dtf.maxbytes
        END as max_bytes
      FROM
        sys.dba_temp_files dtf
    ) Y
  GROUP BY Y.name
) Z, sys.dba_tablespaces dt
WHERE
  Z.name = dt.tablespace_name
`)
	if err != nil {
		return err
	}
	defer rows.Close()
	tablespaceBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "tablespace", "bytes"),
		"Generic counter metric of tablespaces bytes in Oracle.",
		[]string{"tablespace", "type"}, nil,
	)
	tablespaceMaxBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "tablespace", "max_bytes"),
		"Generic counter metric of tablespaces max bytes in Oracle.",
		[]string{"tablespace", "type"}, nil,
	)
	tablespaceFreeBytesDesc := prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "tablespace", "free"),
		"Generic counter metric of tablespaces free bytes in Oracle.",
		[]string{"tablespace", "type"}, nil,
	)

	for rows.Next() {
		var tablespace_name string
		var status string
		var contents string
		var extent_management string
		var bytes float64
		var max_bytes float64
		var bytes_free float64

		if err := rows.Scan(&tablespace_name, &status, &contents, &extent_management, &bytes, &max_bytes, &bytes_free); err != nil {
			return err
		}
		ch <- prometheus.MustNewConstMetric(tablespaceBytesDesc, prometheus.GaugeValue, float64(bytes), tablespace_name, contents)
		ch <- prometheus.MustNewConstMetric(tablespaceMaxBytesDesc, prometheus.GaugeValue, float64(max_bytes), tablespace_name, contents)
		ch <- prometheus.MustNewConstMetric(tablespaceFreeBytesDesc, prometheus.GaugeValue, float64(bytes_free), tablespace_name, contents)
	}
	return nil
}

// ScrapeOsStats collects osstats from the v$wosstat view.
func ScrapeOsStats(db *sql.DB, ch chan<- prometheus.Metric) error {
	var (
		rows *sql.Rows
		err  error
	)
	rows, err = db.Query("select stat_name, value from v$osstat where cumulative = 'YES'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var value float64
		if err := rows.Scan(&name, &value); err != nil {
			return err
		}
		name = cleanName(name)
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(prometheus.BuildFQName(namespace, "os_stats", name),
				"Cumulative measures from V$OSSTAT", []string{}, nil),
			prometheus.GaugeValue,
			value,
		)
	}
	return nil
}

// Oracle gives us some ugly names back. This function cleans things up for Prometheus.
func cleanName(s string) string {
	s = strings.Replace(s, " ", "_", -1) // Remove spaces
	s = strings.Replace(s, "(", "", -1)  // Remove open parenthesis
	s = strings.Replace(s, ")", "", -1)  // Remove close parenthesis
	s = strings.Replace(s, "/", "", -1)  // Remove forward slashes
	s = strings.ToLower(s)
	return s
}

func main() {
	flag.Parse()
	log.Infoln("Starting oracledb_exporter " + Version)
	dsn := os.Getenv("DATA_SOURCE_NAME")
	exporter := NewExporter(dsn)
	prometheus.MustRegister(exporter)
	http.Handle(*metricPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPage)
	})
	log.Infoln("Listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}

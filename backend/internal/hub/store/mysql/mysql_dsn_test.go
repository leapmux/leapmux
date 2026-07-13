package mysql

import (
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMySQLDSNForcesUTCAndPreservesOtherParams(t *testing.T) {
	normalized, err := normalizeMySQLDSN(
		"user:pass@tcp(localhost:3306)/leapmux" +
			"?parseTime=false" +
			"&loc=Local" +
			"&time_zone=%27US%2FPacific%27" +
			"&clientFoundRows=false" +
			"&timeout=2s" +
			"&autocommit=1",
	)
	require.NoError(t, err)

	cfg, err := mysqldriver.ParseDSN(normalized)
	require.NoError(t, err)
	require.True(t, cfg.ParseTime)
	require.Same(t, time.UTC, cfg.Loc)
	// clientFoundRows is forced on even though the input DSN set it false, so
	// rows-affected counts matched (not changed) rows -- consistent with
	// sqlite/postgres and required by the shared rows-affected == 1 guards.
	require.True(t, cfg.ClientFoundRows)
	require.Equal(t, 2*time.Second, cfg.Timeout)
	require.Equal(t, "1", cfg.Params["autocommit"])
	require.Equal(t, "'+00:00'", cfg.Params["time_zone"])
}

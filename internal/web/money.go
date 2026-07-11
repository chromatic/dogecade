package web

import (
	"fmt"
	"strings"
)

const koinuPerDoge = 100_000_000

// formatKoinuAsDoge renders a koinu amount (1 DOGE = 100,000,000 koinu, same
// relationship as satoshi/BTC) as a trimmed decimal DOGE string, e.g.
// 150000000 -> "1.5", 100000000 -> "1", 1 -> "0.00000001". Used everywhere a
// human (admin or customer) needs to read or reason about a price, since
// nobody thinks in koinu and raw-integer prices are an easy way to
// fat-finger an amount by several orders of magnitude.
func formatKoinuAsDoge(koinu int64) string {
	whole := koinu / koinuPerDoge
	frac := koinu % koinuPerDoge
	if frac == 0 {
		return fmt.Sprintf("%d", whole)
	}
	fracStr := strings.TrimRight(fmt.Sprintf("%08d", frac), "0")
	return fmt.Sprintf("%d.%s", whole, fracStr)
}

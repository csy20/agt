package usage

// Prices are micro-dollars per million tokens. Callers pass them in; the
// binary does not assume a particular model SKU.
type Prices struct {
	InputPerMillion  int64
	OutputPerMillion int64
}

// CostMicros is tokens * pricePerMillion / 1_000_000, in micro-dollars.
func CostMicros(tokens, pricePerMillion int64) int64 {
	if tokens <= 0 || pricePerMillion <= 0 {
		return 0
	}
	return tokens * pricePerMillion / 1_000_000
}

// CentsToMicros converts a run budget expressed in cents.
func CentsToMicros(cents int64) int64 {
	return cents * 10_000
}

// USD renders micro-dollars with six decimal places.
func USD(micros int64) string {
	sign := ""
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	return sign + "$" + itoa(micros/1_000_000) + "." + pad6(micros%1_000_000)
}

func itoa(n int64) string {
	return fmtInt(n)
}

func pad6(n int64) string {
	s := fmtInt(n)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}

func fmtInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

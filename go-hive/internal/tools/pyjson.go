package tools

// Python-compatible scalar formatting shared by the tool-result serializers.
//
// Python's json.dumps writes floats with repr(float): the shortest string that
// round-trips, positional for exponents in [-4, 16) and in exponent form
// otherwise ("1e+16", "1e-07"). Go's strconv 'g' format switches to exponent
// form much earlier (1.795230196937622e+09 for 1795230213.240235), so a tool
// payload built with %v / 'g' is NOT byte-compatible with the reference.
// Every JSON payload a tool returns to the model goes through
// PythonJSONDumps, so the float literal has to match Python exactly.

import (
	"strconv"
	"strings"
)

// FormatPyFloat renders a float64 the way Python's repr(float) does.
//
//	1        -> "1.0"        1e6    -> "1000000.0"
//	3.5      -> "3.5"        1e16   -> "1e+16"
//	1e-7     -> "1e-07"      1795230213.240235 -> "1795230213.240235"
func FormatPyFloat(value float64) string {
	shortest := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(shortest, "eE") {
		if !strings.Contains(shortest, ".") {
			return shortest + ".0"
		}
		return shortest
	}
	exponent, err := strconv.Atoi(shortest[strings.IndexAny(shortest, "eE")+1:])
	if err != nil || exponent < -4 || exponent >= 16 {
		return fixPyExponent(strconv.FormatFloat(value, 'e', -1, 64))
	}
	positional := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(positional, ".") {
		positional += ".0"
	}
	return positional
}

// fixPyExponent pads the exponent to at least two digits ("1e+6" -> "1e+06"),
// which is what Python prints.
func fixPyExponent(value string) string {
	index := strings.IndexAny(value, "eE")
	if index < 0 {
		return value
	}
	mantissa := value[:index]
	exponent := value[index+1:]
	sign := ""
	if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
		sign = exponent[:1]
		exponent = exponent[1:]
	}
	if len(exponent) == 1 {
		exponent = "0" + exponent
	}
	return mantissa + "e" + sign + exponent
}

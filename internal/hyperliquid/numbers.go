package hyperliquid

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func formatSize(value float64, decimals int) (string, error) {
	if !finitePositive(value) {
		return "", errors.New("amount must be finite and positive")
	}
	if decimals < 0 || decimals > 8 {
		return "", fmt.Errorf("unsupported size precision %d", decimals)
	}
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if separator := strings.IndexByte(text, '.'); separator >= 0 {
		fraction := strings.TrimRight(text[separator+1:], "0")
		if len(fraction) > decimals {
			return "", fmt.Errorf("amount has more than %d decimal places", decimals)
		}
	}
	return text, nil
}

func formatPrice(value float64, sizeDecimals int) (string, float64, error) {
	if !finitePositive(value) {
		return "", 0, errors.New("price must be finite and positive")
	}
	magnitude := math.Floor(math.Log10(math.Abs(value)))
	significantScale := math.Pow10(4 - int(magnitude))
	rounded := math.Round(value*significantScale) / significantScale
	maxDecimals := 6 - sizeDecimals
	decimalScale := math.Pow10(maxDecimals)
	rounded = math.Round(rounded*decimalScale) / decimalScale
	if !finitePositive(rounded) {
		return "", 0, errors.New("price rounds to zero")
	}
	decimals := maxDecimals
	if decimals < 0 {
		decimals = 0
	}
	return normalizedFloat(rounded, decimals), rounded, nil
}

func normalizedFloat(value float64, decimals int) string {
	text := strconv.FormatFloat(value, 'f', decimals, 64)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "-0" || text == "" {
		return "0"
	}
	return text
}

func parseFinite(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
		return 0, fmt.Errorf("invalid numeric value %q", value)
	}
	return parsed, nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

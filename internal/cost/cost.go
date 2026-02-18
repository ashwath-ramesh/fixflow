package cost

import "fmt"

// Rate holds per-1M-token pricing in USD.
type Rate struct {
	Input  float64 // USD per 1M input tokens
	Output float64 // USD per 1M output tokens
}

// DefaultRates contains hardcoded per-provider pricing.
var DefaultRates = map[string]Rate{
	"claude": {Input: 3.00, Output: 15.00},
	"codex":  {Input: 3.00, Output: 12.00},
}

// Calculate returns the estimated cost in USD for the given token counts.
func Calculate(provider string, inputTokens, outputTokens int) float64 {
	rate, ok := DefaultRates[provider]
	if !ok {
		return 0
	}
	inCost := float64(inputTokens) / 1_000_000 * rate.Input
	outCost := float64(outputTokens) / 1_000_000 * rate.Output
	return inCost + outCost
}

// FormatUSD formats a cost as a dollar string (e.g. "$0.42" or "$1.23").
func FormatUSD(cost float64) string {
	return fmt.Sprintf("$%.2f", cost)
}

// FormatRate returns a display string for a provider's rate (e.g. "$3.00/$15.00 per 1M tokens").
func FormatRate(provider string) string {
	rate, ok := DefaultRates[provider]
	if !ok {
		return "unknown pricing"
	}
	return fmt.Sprintf("$%.2f/$%.2f per 1M tokens", rate.Input, rate.Output)
}

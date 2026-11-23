package query

import "math"

const ScoreDecimalPlaces = 4

// RoundScore rounds a relevance score to ScoreDecimalPlaces, half away from zero.
func RoundScore(score float64) float64 {
	pow := math.Pow10(ScoreDecimalPlaces)
	return math.Round(score*pow) / pow
}

func CalcSafeScore(rawScore *float64) float64 {
	maxScore := 1.0
	if rawScore != nil && *rawScore > 0 {
		maxScore = *rawScore
	}
	return maxScore
}

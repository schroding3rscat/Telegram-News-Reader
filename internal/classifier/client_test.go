package classifier

import (
	"testing"
)

func TestParseResultValidatesEvidence(t *testing.T) {
	t.Parallel()
	source := "Купите подписку со скидкой по промокоду NEWS."
	result, err := parseResult(`{
		"is_ad": true,
		"confidence": 0.96,
		"topic": "подписки",
		"is_uninteresting": false,
		"evidence_spans": ["Купите подписку", "промокоду NEWS"],
		"reason": "прямая продажа"
	}`, source)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsAd || result.Confidence != 0.96 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestParseResultRejectsHallucinatedEvidence(t *testing.T) {
	t.Parallel()
	_, err := parseResult(`{
		"is_ad": true,
		"confidence": 0.9,
		"topic": "товары",
		"is_uninteresting": false,
		"evidence_spans": ["этого текста нет"],
		"reason": "реклама"
	}`, "Обычная новость")
	if err == nil {
		t.Fatal("expected invalid evidence to be rejected")
	}
}

func TestCheapSignals(t *testing.T) {
	t.Parallel()
	signals := CheapSignals("Реклама. Используйте промокод NEWS")
	if len(signals) < 2 {
		t.Fatalf("expected ad signals, got %v", signals)
	}
}

package ingest

import (
	"github.com/tadjov/sengi/pkg/apis/preprocess"
)

// ruleHit is a rule firing on one article at its signal's value.
type ruleHit struct {
	rule  string
	field string
	value int64
}

// ruleOutcome is what the rules decided for each kept article, by its position among them.
type ruleOutcome struct {
	drops  map[int]ruleHit
	clears map[int][]ruleHit
}

// applyRules runs the rules in order. A drop wins over any clear, the first rule to drop
// or to clear a field names the edit, and clearing a field that is already empty changes nothing.
func applyRules(rules []preprocess.Rule, signals map[string]signalValues, filled func(k int, field string) bool) ruleOutcome {
	outcome := ruleOutcome{drops: map[int]ruleHit{}, clears: map[int][]ruleHit{}}
	cleared := map[int]map[string]bool{}
	for _, rule := range rules {
		for k, value := range signals[rule.When.Signal].values {
			if value < rule.When.AtLeast {
				continue
			}
			hit := ruleHit{rule: rule.Name, field: rule.Field, value: value}
			switch rule.Action {
			case preprocess.ActionDrop:
				if _, dropped := outcome.drops[k]; !dropped {
					outcome.drops[k] = hit
				}
			case preprocess.ActionClear:
				if cleared[k][rule.Field] || !filled(k, rule.Field) {
					continue
				}
				if cleared[k] == nil {
					cleared[k] = map[string]bool{}
				}
				cleared[k][rule.Field] = true
				outcome.clears[k] = append(outcome.clears[k], hit)
			}
		}
	}
	for k := range outcome.drops {
		delete(outcome.clears, k)
	}
	return outcome
}

func (o ruleOutcome) counts(rules []preprocess.Rule) (dropped, cleared map[string]int64) {
	dropped, cleared = map[string]int64{}, map[string]int64{}
	for _, rule := range rules {
		switch rule.Action {
		case preprocess.ActionDrop:
			dropped[ruleDropKey(rule.Name)] = 0
		case preprocess.ActionClear:
			cleared[rule.Name] = 0
		}
	}
	for _, hit := range o.drops {
		dropped[ruleDropKey(hit.rule)]++
	}
	for _, hits := range o.clears {
		for _, hit := range hits {
			cleared[hit.rule]++
		}
	}
	return dropped, cleared
}

func ruleDropKey(rule string) string {
	return string(stageRule) + "/" + rule
}

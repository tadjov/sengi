package judgment

// GateAdmits reports whether doc passes q's filters and its phrase or boolean
// gate. Every gated judge grades a refused doc 0 without asking a model.
func GateAdmits(q GradingQuery, doc GradingDoc) (bool, error) {
	tq, err := readTermQuery(q)
	if err != nil {
		return false, err
	}
	return tq.admits(analyzeDoc(doc)), nil
}

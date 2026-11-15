package judgment

// GateAdmits reports whether doc passes q's filters and phrase or boolean gate.
func GateAdmits(q GradingQuery, doc GradingDoc) (bool, error) {
	tq, err := readTermQuery(q)
	if err != nil {
		return false, err
	}
	return tq.admits(analyzeDoc(doc)), nil
}

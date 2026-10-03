package modelevaluation

import "errors"

// ExpectedCalls binds ledger cardinality to a reviewed corpus version. Recognition
// of the prepared pilot does not enable HTTP admission; SupportedCorpus owns that.
func ExpectedCalls(version string) (int, error) {
	switch version {
	case CorpusVersion, CacheCorpusVersion:
		return MaxCalls, nil
	case ImplementationPilotVersion:
		return 3, nil
	default:
		return 0, errors.New("unknown evaluation corpus cardinality")
	}
}

package v1

// PostgresProfile defines possible values and our defaults for the zalando operator deployment
// will be configured during postgres-controller deployment
// TODO should be a CRD as well to keep configuration identical
type PostgresProfile struct {
	Versions        []string
	OperatorVersion string
	// TODO  more
}

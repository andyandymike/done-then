package actions

const helperProtocolVersion = 1

type helperOperation string

const (
	helperPreflight helperOperation = "preflight"
	helperSchedule  helperOperation = "schedule"
	helperCancel    helperOperation = "cancel"
	helperReconcile helperOperation = "reconcile"
)

type helperRequest struct {
	SchemaVersion int             `json:"schema_version"`
	Operation     helperOperation `json:"operation"`
	Request       *PowerRequest   `json:"request,omitempty"`
	Receipt       *Receipt        `json:"receipt,omitempty"`
}

type helperResponse struct {
	SchemaVersion int              `json:"schema_version"`
	OK            bool             `json:"ok"`
	ErrorCode     string           `json:"error_code,omitempty"`
	Message       string           `json:"message,omitempty"`
	Capabilities  *Capabilities    `json:"capabilities,omitempty"`
	Receipt       *Receipt         `json:"receipt,omitempty"`
	CancelResult  *CancelResult    `json:"cancel_result,omitempty"`
	Reconcile     *ReconcileResult `json:"reconcile_result,omitempty"`
}

type powerHelperClient interface {
	Call(operation helperOperation, request *PowerRequest, receipt *Receipt) (helperResponse, error)
}

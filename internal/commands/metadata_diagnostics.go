package commands

type metadataDiagnostic struct {
	Level    string `json:"level"`
	Code     string `json:"code"`
	Artifact string `json:"artifact"`
	URL      string `json:"url,omitempty"`
	Message  string `json:"message"`
}

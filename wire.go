package prusalink

// The PrusaLink API this driver reads. The fields are the ones in the published
// OpenAPI file of PrusaLink (github.com/prusa3d/Prusa-Link-Web, spec/openapi.yaml).
// A number is a pointer, so a field the printer did not send writes no patch.
const (
	pathVersion = "/api/version"
	pathInfo    = "/api/v1/info"
	pathStatus  = "/api/v1/status"
	pathJob     = "/api/v1/job"
)

// versionReply is GET /api/version. printer is the type code of the machine,
// such as 1.3.1 for an MK3S.
type versionReply struct {
	API      string `json:"api"`
	Server   string `json:"server"`
	Text     string `json:"text"`
	Firmware string `json:"firmware"`
	Printer  string `json:"printer"`
	Hostname string `json:"hostname"`
}

// infoReply is GET /api/v1/info.
type infoReply struct {
	Name           string   `json:"name"`
	Hostname       string   `json:"hostname"`
	Serial         string   `json:"serial"`
	NozzleDiameter *float64 `json:"nozzle_diameter"`
}

// statusReply is GET /api/v1/status. job is absent while nothing prints.
type statusReply struct {
	Job     *statusJob    `json:"job"`
	Printer statusPrinter `json:"printer"`
}

type statusJob struct {
	ID            *int64   `json:"id"`
	Progress      *float64 `json:"progress"`
	TimePrinting  *float64 `json:"time_printing"`
	TimeRemaining *float64 `json:"time_remaining"`
}

type statusPrinter struct {
	State        string   `json:"state"`
	TempNozzle   *float64 `json:"temp_nozzle"`
	TargetNozzle *float64 `json:"target_nozzle"`
	TempBed      *float64 `json:"temp_bed"`
	TargetBed    *float64 `json:"target_bed"`
	AxisX        *float64 `json:"axis_x"`
	AxisY        *float64 `json:"axis_y"`
	AxisZ        *float64 `json:"axis_z"`
	Flow         *float64 `json:"flow"`
	Speed        *float64 `json:"speed"`
	FanHotend    *float64 `json:"fan_hotend"`
	FanPrint     *float64 `json:"fan_print"`
}

// jobReply is GET /api/v1/job. It answers 204 with no body while nothing
// prints, which leaves the reply empty.
type jobReply struct {
	ID   *int64 `json:"id"`
	File struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	} `json:"file"`
}

// name is the file the job prints, as a person reads it.
func (j jobReply) name() string {
	if j.File.DisplayName != "" {
		return j.File.DisplayName
	}

	return j.File.Name
}

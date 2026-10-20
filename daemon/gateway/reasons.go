package gateway

// Reason is the name of a refusal of the gateway. contract/testdata/page-reasons.json lists them with the sentence a
// person reads; the tests of this package, of sdk/web and of sdk/goclient all check themselves against that file.
//
// A refusal carries its Reason as `reason`. A Reason that refines an older one (legacyOf) travels as `code` next to the
// one it refines, which stays in `reason`: a page SDK written before the refinement keeps seeing a reason it knows and
// has a sentence for, and a newer one reads `code` for the better sentence.
type Reason string

const (
	ReasonPeer      Reason = "peer"      // the caller's process belongs to another OS user
	ReasonHost      Reason = "host"      // the Host header is not the gateway's own address
	ReasonOrigin    Reason = "origin"    // no Origin header, or not an origin a browser sends
	ReasonNotFound  Reason = "not-found" // no such request
	ReasonError     Reason = "error"     // the gateway could not read what it keeps
	ReasonMalformed Reason = "malformed" // the body is not a request it understands
	ReasonDenied    Reason = "denied"    // the user said no
	ReasonTimeout   Reason = "timeout"   // the user did not answer in time
	ReasonCooldown  Reason = "cooldown"  // this origin was refused a moment ago
	ReasonLocked    Reason = "locked"    // refines cooldown: refusals of several origins locked every dialog
	ReasonBusy      Reason = "busy"      // another dialog is open
	ReasonNoDialog  Reason = "no-dialog" // this computer cannot ask
	ReasonNoFolder  Reason = "no-folder" // refines no-dialog: the user allowed without choosing a folder
	ReasonPairing   Reason = "pairing"   // the app refused the pairing code
	ReasonNotSaved  Reason = "not-saved" // refines pairing: the grant could not be saved on this computer
)

// legacyOf is the Reason a refinement refines.
var legacyOf = map[Reason]Reason{
	ReasonLocked:   ReasonCooldown,
	ReasonNoFolder: ReasonNoDialog,
	ReasonNotSaved: ReasonPairing,
}

// Reasons is every Reason the gateway can send, plain ones and refinements.
func Reasons() []Reason {
	return []Reason{
		ReasonPeer, ReasonHost, ReasonOrigin, ReasonNotFound, ReasonError, ReasonMalformed,
		ReasonDenied, ReasonTimeout, ReasonCooldown, ReasonLocked, ReasonBusy,
		ReasonNoDialog, ReasonNoFolder, ReasonPairing, ReasonNotSaved,
	}
}

// LegacyOf is the Reason that r refines, which a refusal of r keeps in `reason`; false when r refines none.
func LegacyOf(r Reason) (Reason, bool) {
	legacy, ok := legacyOf[r]
	return legacy, ok
}

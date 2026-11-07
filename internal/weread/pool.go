package weread

// DefaultPayload is a known-good read payload captured from the web reader
// (the same seed data used by the community scripts). It is used when an
// account has no captured template of its own.
var DefaultPayload = map[string]any{
	"appId": "wb182564874603h266381671",
	"b":     "ce032b305a9bc1ce0b0dd2a",
	"c":     "7f632b502707f6ffaa6bf2e",
	"ci":    27,
	"co":    389,
	"sm":    "19聚会《三体》网友的聚会地点是一处僻静",
	"pr":    74,
	"ps":    "4ee326507a65a465g015fae",
	"pc":    "aab32e207a65a466g010615",
}

// BookPool contains book ids that are known to work with the rotating mode.
var BookPool = []string{
	"36d322f07186022636daa5e", "6f932ec05dd9eb6f96f14b9", "43f3229071984b9343f04a4",
	"d7732ea0813ab7d58g0184b8", "3d03298058a9443d052d409", "4fc328a0729350754fc56d4",
	"a743220058a92aa746632c0", "140329d0716ce81f140468e", "1d9321c0718ff5e11d9afe8",
	"ff132750727dc0f6ff1f7b5", "e8532a40719c4eb7e851cbe", "9b13257072562b5c9b1c8d6",
}

// ChapterPool contains chapter ids that are accepted by the read endpoint.
var ChapterPool = []string{
	"ecc32f3013eccbc87e4b62e", "a87322c014a87ff679a21ea", "e4d32d5015e4da3b7fbb1fa",
	"16732dc0161679091c5aeb1", "8f132430178f14e45fce0f7", "c9f326d018c9f0f895fb5e4",
	"45c322601945c48cce2e120", "d3d322001ad3d9446802347", "65132ca01b6512bd43d90e3",
	"c20321001cc20ad4d76f5ae", "c51323901dc51ce410c121b", "aab325601eaab3238922e53",
	"9bf32f301f9bf31c7ff0a60", "c7432af0210c74d97b01b1c", "70e32fb021170efdf2eca12",
	"6f4322302126f4922f45dec",
}

// DefaultTemplate returns a defensive copy of the built-in template.
func DefaultTemplate() map[string]any {
	out := make(map[string]any, len(DefaultPayload))
	for k, v := range DefaultPayload {
		out[k] = v
	}
	return out
}

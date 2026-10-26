package workflowschema

import _ "embed"

//go:embed go/workflows.csil-schema.cbor
var Descriptor []byte

const Digest = "250e6b365f5517cbcfca9260224f36a0a5aa3b3e38a56e66666c21a074c48269"

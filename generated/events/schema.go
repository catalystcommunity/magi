package eventschema

import _ "embed"

//go:embed go/events.csil-schema.cbor
var Descriptor []byte

const Digest = "e5f820dcb3bc279851202aff16314ccf75f1210de0a8a57ac7e224b838a2c0fc"

package magi

import _ "embed"

// Descriptor is the generated, target-independent CSIL schema.
//
//go:embed generated/go/magi.csil-schema.cbor
var Descriptor []byte

const ProtocolVersion = "0alpha1"
const DescriptorDigest = "5e4a4c5f597e1937074632c85c0e8b18901e6289655a2bc86d66ba132fd0d764"

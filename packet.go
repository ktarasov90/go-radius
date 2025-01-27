package radius

import (
	"bytes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
)

// maximum RADIUS packet size
const maxPacketSize = 4095

// Code specifies the kind of RADIUS packet
type Code byte

// Codes which are defined in RFC 2865
const (
	CodeAccessRequest Code = 1
	CodeAccessAccept  Code = 2
	CodeAccessReject  Code = 3

	CodeAccountingRequest  Code = 4
	CodeAccountingResponse Code = 5

	CodeAccessChallenge Code = 11

	CodeStatusServer Code = 12
	CodeStatusClient Code = 13

	CodeDisconnectRequest Code = 40
	CodeDisconnectACK     Code = 41
	CodeDisconnectNAK     Code = 42

	CodeCoARequest Code = 43
	CodeCoAACK     Code = 44
	CodeCoANAK     Code = 45

	CodeReserved Code = 255
)

// Packet defines a RADIUS packet.
type Packet struct {
	Code          Code
	Identifier    byte
	Authenticator [16]byte
	Secret        []byte

	Raw        *[]byte
	Dictionary *Dictionary
	Attributes []*Attribute
}

// New returns a new packet with the given code and secret. The identifier and
// authenticator are filled with random data, and the dictionary is set to
// Builtin. nil is returned if not enough random data could be generated.
func New(code Code, secret []byte) *Packet {
	var buff [17]byte

	if _, err := rand.Read(buff[:]); err != nil {
		return nil
	}

	packet := &Packet{
		Code:       code,
		Identifier: buff[0],
		Secret:     secret,
		Dictionary: Builtin,
	}

	copy(packet.Authenticator[:], buff[1:])
	return packet
}

type ParseFunc func (data, secret []byte, dictionary *Dictionary) (*Packet, error)

// Parse parses a RADIUS packet from wire data, using the given shared secret
// and dictionary. nil and an error is returned if there is a problem parsing
// the packet.
//
// Note: this function does not validate the authenticity of a packet.
// Ensuring a packet's authenticity should be done using the IsAuthentic
// method.
func Parse(data, secret []byte, dictionary *Dictionary) (*Packet, error) {
	if len(data) < 20 {
		return nil, errors.New("radius: packet must be at least 20 bytes long")
	}

	packet := &Packet{
		Code:       Code(data[0]),
		Raw:        &data,
		Identifier: data[1],
		Secret:     secret,
		Dictionary: dictionary,
	}

	length := binary.BigEndian.Uint16(data[2:4])
	if length < 20 || length > maxPacketSize {
		return nil, errors.New("radius: invalid packet length")
	}

	copy(packet.Authenticator[:], data[4:20])

	// Attributes
	attributes := data[20:]
	for len(attributes) > 0 {
		if len(attributes) < 2 {
			return nil, errors.New("radius: attribute must be at least 2 bytes long")
		}

		attrLength := attributes[1]
		if attrLength < 1 || attrLength > 253 || len(attributes) < int(attrLength) {
			return nil, errors.New("radius: invalid attribute length")
		}

		attrType := attributes[0]
		attrValue := attributes[2:attrLength]

		codec := dictionary.Codec(attrType)
		decoded, err := codec.Decode(packet, attrValue)
		if err != nil {
			return nil, err
		}

		attr := &Attribute{
			Type:  attrType,
			Value: decoded,
		}

		packet.Attributes = append(packet.Attributes, attr)
		attributes = attributes[attrLength:]
	}

	// TODO: validate that the given packet (by code) has all the required attributes, etc.
	return packet, nil
}

// IsAuthentic returns if the packet is an authenticate response to the given
// request packet. Calling this function is only valid if both:
//  - p.code is one of:
//      CodeAccessAccept
//      CodeAccessReject
//      CodeAccountingRequest
//      CodeAccountingResponse
//      CodeAccessChallenge
// 		CodeCoAACK, CodeCOANAK
//  	CodeDisconnectACK, CodeDisconnectNAK
//  - p.Authenticator contains the calculated authenticator
func (p *Packet) IsAuthentic(request *Packet) bool {
	switch p.Code {
	case CodeAccessAccept, CodeAccessReject, CodeAccountingRequest, CodeAccessChallenge, CodeCoAACK, CodeCoANAK, CodeDisconnectACK, CodeDisconnectNAK:
		wire, err := p.Encode()
		if err != nil {
			return false
		}

		hash := md5.New()
		hash.Write(wire[0:4])

		switch p.Code {
		case CodeAccountingRequest:
			var nul [16]byte
			hash.Write(nul[:])
			break

		default:
			hash.Write(request.Authenticator[:])
			break
		}

		hash.Write(wire[20:])
		hash.Write(request.Secret)

		var sum [md5.Size]byte
		return bytes.Equal(hash.Sum(sum[0:0]), p.Authenticator[:])
	}

	return false
}

// ResponseAuthenticator calculates the response authenticator field
func (p *Packet) ResponseAuthenticator() (sum []byte, err error) {
	var wire []byte

	switch p.Code {
	case CodeAccessAccept, CodeAccessReject, CodeAccountingRequest, CodeAccessChallenge:
	case CodeCoARequest, CodeCoAACK, CodeCoANAK, CodeDisconnectRequest, CodeDisconnectACK, CodeDisconnectNAK:
		if wire, err = p.Encode(); err != nil {
			return
		}

		hash := md5.New()
		hash.Write(wire[0:4])

		if p.Code == CodeAccountingRequest {
			var nul [16]byte
			hash.Write(nul[:])
		} else {
			hash.Write(p.Authenticator[:])
		}

		hash.Write(wire[20:])
		hash.Write(p.Secret)
		sum = hash.Sum(sum[0:0])

		return
	}

	err = errors.New("Unknown packet code")
	return
}

// ClearAttributes removes all of the packet's attributes.
func (p *Packet) ClearAttributes() {
	p.Attributes = nil
}

// Value returns the value of the first attribute whose dictionary name matches
// the given name. nil is returned if no such attribute exists.
func (p *Packet) Value(name string) interface{} {
	if attr := p.Attr(name); attr != nil {
		return attr.Value
	}
	return nil
}

// Attr returns the first attribute whose dictionary name matches the given
// name. nil is returned if no such attribute exists.
func (p *Packet) Attr(name string) *Attribute {
	for _, attr := range p.Attributes {
		if attrName, ok := p.Dictionary.Name(attr.Type); ok && attrName == name {
			return attr
		}
	}
	return nil
}

// Values returns a slice of all attributes' values with given name
func (p *Packet) Values(name string) (values []interface{}) {
	for _, attr := range p.Attributes {
		if attrName, ok := p.Dictionary.Name(attr.Type); ok && attrName == name {
			values = append(values, attr.Value)
		}
	}
	return
}

// String returns the string representation of the value of the first attribute
// whose dictionary name matches the given name. The following rules are used
// for converting the attribute value to a string:
//
//  - If no such attribute exists with the given dictionary name, "" is
//    returned
//  - If the attribute's Codec implements AttributeStringer,
//    AttributeStringer.String(value) is returned
//  - If the value implements fmt.Stringer, value.String() is returned
//  - If the value is string, itself is returned
//  - If the value is []byte, string(value) is returned
//  - Otherwise, "" is returned
func (p *Packet) String(name string) string {
	attr := p.Attr(name)
	if attr == nil {
		return ""
	}
	value := attr.Value

	if codec := p.Dictionary.Codec(attr.Type); codec != nil {
		if stringer, ok := codec.(AttributeStringer); ok {
			return stringer.String(value)
		}
	}

	if stringer, ok := value.(interface {
		String() string
	}); ok {
		return stringer.String()
	}

	if str, ok := value.(string); ok {
		return str
	}

	if raw, ok := value.([]byte); ok {
		return string(raw)
	}
	return ""
}

// Add adds an attribute whose dictionary name matches the given name.
func (p *Packet) Add(name string, value interface{}) error {
	attr, err := p.Dictionary.Attr(name, value)
	if err != nil {
		return err
	}
	p.AddAttr(attr)
	return nil
}

// AddAttr adds the given attribute to the packet.
func (p *Packet) AddAttr(attribute *Attribute) {
	p.Attributes = append(p.Attributes, attribute)
}

// AddAttrs adds several attributes to th epacket.
func (p *Packet) AddAttrs(attributes []*Attribute) {
	for _, attr := range attributes {
		p.Attributes = append(p.Attributes, attr)
	}
}

// Set sets the value of the first attribute whose dictionary name matches the
// given name. If no such attribute exists, a new attribute is added
func (p *Packet) Set(name string, value interface{}) error {
	for _, attr := range p.Attributes {
		if attrName, ok := p.Dictionary.Name(attr.Type); ok && attrName == name {
			codec := p.Dictionary.Codec(attr.Type)
			if transformer, ok := codec.(AttributeTransformer); ok {
				transformed, err := transformer.Transform(value)
				if err != nil {
					return err
				}
				attr.Value = transformed
				return nil
			}
			attr.Value = value
			return nil
		}
	}
	return p.Add(name, value)
}

// PAP returns the User-Name and User-Password attributes of an Access-Request
// packet.
//
// If packet's code is Access-Request, and the packet has a User-Name and
// User-Password attribute, ok is true. Otherwise, it is false.
func (p *Packet) PAP() (username, password string, ok bool) {
	if p.Code != CodeAccessRequest {
		return
	}
	user := p.Value("User-Name")
	if user == nil {
		return
	}
	pass := p.Value("User-Password")
	if pass == nil {
		return
	}
	if userStr, valid := user.(string); valid {
		username = userStr
	} else {
		return
	}
	if passStr, valid := pass.(string); valid {
		password = passStr
	} else {
		username = ""
		return
	}
	ok = true
	return
}

// Encode encodes the packet to wire format. If there is an error encoding the
// packet, nil and an error is returned.
func (p *Packet) Encode() ([]byte, error) {
	var bufferAttrs bytes.Buffer

	for _, attr := range p.Attributes {
		codec := p.Dictionary.Codec(attr.Type)
		wire, err := codec.Encode(p, attr.Value)
		if err != nil {
			return nil, err
		}

		if len(wire) > 253 {
			return nil, errors.New("radius: encoded attribute is too long")
		}

		bufferAttrs.WriteByte(attr.Type)
		bufferAttrs.WriteByte(byte(len(wire) + 2))
		bufferAttrs.Write(wire)
	}

	length := 20 + bufferAttrs.Len()
	if length > maxPacketSize {
		return nil, errors.New("radius: encoded packet is too long")
	}

	var buffer bytes.Buffer
	buffer.Grow(length)
	buffer.WriteByte(byte(p.Code))
	buffer.WriteByte(p.Identifier)
	binary.Write(&buffer, binary.BigEndian, uint16(length))

	switch p.Code {
	case CodeAccessRequest, CodeStatusServer:
		buffer.Write(p.Authenticator[:])
		break

	case CodeCoARequest, CodeDisconnectRequest, CodeAccessAccept, CodeAccessReject, CodeAccountingRequest, CodeAccountingResponse, CodeAccessChallenge, CodeCoAACK, CodeCoANAK, CodeDisconnectACK, CodeDisconnectNAK:
		hash := md5.New()
		hash.Write(buffer.Bytes())

		switch p.Code {
		case CodeAccountingRequest, CodeCoARequest, CodeDisconnectRequest:
			var nul [16]byte
			hash.Write(nul[:])
			break

		default:
			hash.Write(p.Authenticator[:])
			break
		}

		hash.Write(bufferAttrs.Bytes())
		hash.Write(p.Secret)

		var sum [16]byte
		buffer.Write(hash.Sum(sum[0:0]))

		// We overwrite the original authenticator because it will be used in IsAuthentic() to authenticate a reply
		switch p.Code {
		case CodeCoARequest, CodeDisconnectRequest:
			copy(p.Authenticator[:], sum[:])
			break
		}

		break

	default:
		return nil, errors.New("radius: unknown Packet code")
	}

	buffer.ReadFrom(&bufferAttrs)

	return buffer.Bytes(), nil
}

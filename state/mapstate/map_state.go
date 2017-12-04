// Package mapstate implements the State interface for IPFS Cluster by using
// a map to keep track of the consensus-shared state.
package mapstate

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"sync"

	msgpack "github.com/multiformats/go-multicodec/msgpack"

	cid "github.com/ipfs/go-cid"
	logging "github.com/ipfs/go-log"
	"github.com/ipfs/ipfs-cluster/api"
)

// Version is the map state Version. States with old versions should
// perform an upgrade before.
const Version = 3

var logger = logging.Logger("mapstate")

// MapState is a very simple database to store the state of the system
// using a Go map. It is thread safe. It implements the State interface.
type MapState struct {
	pinMux  sync.RWMutex
	PinMap  map[string]api.PinSerial
	Version int
}

// NewMapState initializes the internal map and returns a new MapState object.
func NewMapState() *MapState {
	return &MapState{
		PinMap:  make(map[string]api.PinSerial),
		Version: Version,
	}
}

// Add adds a Pin to the internal map.
func (st *MapState) Add(c api.Pin) error {
	st.pinMux.Lock()
	defer st.pinMux.Unlock()
	st.PinMap[c.Cid.String()] = c.ToSerial()
	return nil
}

// Rm removes a Cid from the internal map.
func (st *MapState) Rm(c *cid.Cid) error {
	st.pinMux.Lock()
	defer st.pinMux.Unlock()
	delete(st.PinMap, c.String())
	return nil
}

// Get returns Pin information for a CID.
func (st *MapState) Get(c *cid.Cid) api.Pin {
	st.pinMux.RLock()
	defer st.pinMux.RUnlock()
	pins, ok := st.PinMap[c.String()]
	if !ok { // make sure no panics
		return api.Pin{}
	}
	return pins.ToPin()
}

// Has returns true if the Cid belongs to the State.
func (st *MapState) Has(c *cid.Cid) bool {
	st.pinMux.RLock()
	defer st.pinMux.RUnlock()
	_, ok := st.PinMap[c.String()]
	return ok
}

// List provides the list of tracked Pins.
func (st *MapState) List() []api.Pin {
	st.pinMux.RLock()
	defer st.pinMux.RUnlock()
	cids := make([]api.Pin, 0, len(st.PinMap))
	for _, v := range st.PinMap {
		if v.Cid == "" {
			continue
		}
		cids = append(cids, v.ToPin())
	}
	return cids
}

// Snapshot dumps the MapState to the given writer, in pretty json
// format.
func (st *MapState) Snapshot(w io.Writer) error {
	st.pinMux.RLock()
	defer st.pinMux.RUnlock()
	enc := json.NewEncoder(w)
	enc.SetIndent("", "    ")
	return enc.Encode(st)
}

// Restore restores a snapshot from the state's internal bytes. It should
// migrate the format if it is not compatible with the current version.
func (st *MapState) Restore(r io.Reader) error {
	bs, err := ioutil.ReadAll(r)
	if err != nil {
		return err
	}
	err = st.Unmarshal(bs)
	if st.Version == Version { // Unmarshal restored for us
		return nil
	}
	bytesNoVersion := bs[1:] // Restore is aware of encoding format
	err = st.migrateFrom(st.Version, bytesNoVersion)
	if err != nil {
		return err
	}
	st.Version = Version
	return nil
}

// GetVersion returns the current version of this state object.
// It is not necessarily up to date
func (st *MapState) GetVersion() int {
	return st.Version
}

// Marshal encodes the state using msgpack
func (st *MapState) Marshal() ([]byte, error) {
	logger.Debugf("Marshal-- Marshalling state of version %d", st.Version)
	buf := new(bytes.Buffer)
	enc := msgpack.Multicodec(msgpack.DefaultMsgpackHandle()).Encoder(buf)
	if err := enc.Encode(st); err != nil {
		return nil, err
	}
	// First byte indicates the version (probably should make this a varint
	// if we stick to this encoding)
	vCodec := make([]byte, 1)
	vCodec[0] = byte(st.Version)
	ret := append(vCodec, buf.Bytes()...)
	logger.Debugf("Marshal-- The final marshaled bytes: %x", ret)
	return ret, nil
}

// Unmarshal decodes the state using msgpack.  It first decodes just
// the version number.  If this is not the current version the bytes
// are stored within the state's internal reader, which can be migrated
// to the current version in a later call to restore.  Note: Out of date
// version is not an error
func (st *MapState) Unmarshal(bs []byte) error {
	// Check version byte
	logger.Debugf("The incoming bytes to unmarshal: %x", bs)
	v := int(bs[0])
	logger.Debugf("The interpreted version: %d", v)
	if v != Version { // snapshot is out of date
		st.Version = v
		return nil
	}

	// snapshot is up to date
	buf := bytes.NewBuffer(bs[1:])
	dec := msgpack.Multicodec(msgpack.DefaultMsgpackHandle()).Decoder(buf)
	if err := dec.Decode(st); err != nil {
		return err
	}
	return nil
}

// Client half of the O-B+ handshake: attach a ticket + request MAC to an
// outgoing data-plane request (the exact header set Verifier checks).
package ticket

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"
)

// SignRequest stamps the full ticket header set onto req: the ticket,
// the sender timestamp, a fresh nonce, and the MAC binding all of it to
// method+path+body.
func SignRequest(req *http.Request, rawTicket string, body []byte, now time.Time) error {
	key, err := MACKey(rawTicket)
	if err != nil {
		return err
	}

	var n [8]byte
	if _, err := rand.Read(n[:]); err != nil {
		return err
	}

	nonce := hex.EncodeToString(n[:])
	sum := sha256.Sum256(body)
	ts := now.Unix()

	req.Header.Set(HeaderTicket, rawTicket)
	req.Header.Set(HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderMAC, ComputeMAC(key, req.Method, req.URL.Path, sum[:], ts, nonce))
	return nil
}

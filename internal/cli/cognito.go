package cli

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bronto-community/bronto-cli/internal/api"
	"github.com/bronto-community/bronto-cli/internal/clierr"
	"github.com/bronto-community/bronto-cli/internal/version"
)

// PROTOTYPE (bronto signup): a minimal Amazon Cognito client — the public
// app client the Bronto web app uses, no AWS SDK. Sign-in is USER_SRP_AUTH
// (USER_PASSWORD_AUTH is disabled for the client).

// signupRegion holds one Bronto region's Cognito user pool and app client.
// The client ids are public (they ship in the web bundle); the US values
// were read from the bundle but never exercised.
type signupRegion struct{ aws, pool, clientID string }

var signupRegions = map[string]signupRegion{
	"eu": {"eu-west-1", "eu-west-1_NgWzeEpmy", "24fsgkap7ml7paoa7vmgrhuq8b"},
	"us": {"us-east-1", "us-east-1_rmSsdIXDF", "44jku5bc18fsf2litfme8g73f1"},
}

// cognitoURLEnv overrides the Cognito endpoint, for tests and recordings.
// Deliberately not a config key and not in any help text.
const cognitoURLEnv = "BRONTO_COGNITO_URL"

type cognitoClient struct {
	endpoint string
	region   signupRegion
	http     *http.Client
}

func newCognitoClient(region string) *cognitoClient {
	r := signupRegions[region]
	endpoint := os.Getenv(cognitoURLEnv)
	if endpoint == "" {
		endpoint = "https://cognito-idp." + r.aws + ".amazonaws.com/"
	}
	return &cognitoClient{endpoint: endpoint, region: r, http: &http.Client{Timeout: 30 * time.Second}}
}

// cognitoError is a Cognito service error ({"__type": ..., "message": ...}).
type cognitoError struct{ Type, Message string }

func (e *cognitoError) Error() string { return e.Type + ": " + e.Message }

// isCognitoErr reports whether err is the Cognito error of the given type.
func isCognitoErr(err error, typ string) bool {
	var ce *cognitoError
	return errors.As(err, &ce) && ce.Type == typ
}

// call issues one Cognito action. Bodies and responses carry passwords'
// derivatives and tokens, so nothing here is ever logged.
func (c *cognitoClient) call(ctx context.Context, action string, body map[string]any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService."+action)
	req.Header.Set("User-Agent", api.UserAgent(version.Version))
	resp, err := api.Fetch(c.http, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Type    string `json:"__type"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(resp.Body, &e)
		if e.Type == "" {
			return clierr.New("signup_failed", fmt.Sprintf("the sign-up service answered HTTP %d to %s", resp.StatusCode, action))
		}
		// "com.amazonaws...#UserNotConfirmedException" -> the bare name.
		return &cognitoError{Type: e.Type[strings.LastIndex(e.Type, "#")+1:], Message: e.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(resp.Body, out)
}

// signIn runs USER_SRP_AUTH and returns the IdToken (the bearer the Bronto
// API accepts). An unconfirmed account fails with UserNotConfirmedException.
func (c *cognitoClient) signIn(ctx context.Context, email, password string) (string, error) {
	aBytes := make([]byte, 128)
	if _, err := rand.Read(aBytes); err != nil {
		return "", err
	}
	a := new(big.Int).SetBytes(aBytes)
	var ch struct {
		ChallengeName       string
		ChallengeParameters map[string]string
	}
	err := c.call(ctx, "InitiateAuth", map[string]any{
		"ClientId": c.region.clientID, "AuthFlow": "USER_SRP_AUTH",
		"AuthParameters": map[string]string{"USERNAME": email, "SRP_A": srpA(a).Text(16)},
	}, &ch)
	if err != nil {
		return "", err
	}
	p := ch.ChallengeParameters
	B, ok := new(big.Int).SetString(p["SRP_B"], 16)
	if !ok {
		return "", clierr.New("signup_failed", "the sign-in challenge was malformed")
	}
	ts := time.Now().UTC().Format("Mon Jan 2 15:04:05 UTC 2006")
	_, poolName, _ := strings.Cut(c.region.pool, "_")
	sig, err := srpSignature(poolName, p["USER_ID_FOR_SRP"], password, p["SALT"], p["SECRET_BLOCK"], ts, a, B)
	if err != nil {
		return "", err
	}
	var res struct {
		ChallengeName        string
		AuthenticationResult struct{ IdToken string }
	}
	err = c.call(ctx, "RespondToAuthChallenge", map[string]any{
		"ClientId": c.region.clientID, "ChallengeName": ch.ChallengeName,
		"ChallengeResponses": map[string]string{
			"USERNAME": p["USER_ID_FOR_SRP"], "PASSWORD_CLAIM_SECRET_BLOCK": p["SECRET_BLOCK"],
			"TIMESTAMP": ts, "PASSWORD_CLAIM_SIGNATURE": sig,
		},
	}, &res)
	if err != nil {
		return "", err
	}
	if res.AuthenticationResult.IdToken == "" {
		return "", clierr.New("signup_failed", "sign-in needs a further step the CLI doesn't support ("+res.ChallengeName+")").
			WithHint("Log in through the web app instead.")
	}
	return res.AuthenticationResult.IdToken, nil
}

// --- SRP-6a as Cognito does it (a port of amazon-cognito-identity-js) ---

// srpN is the 3072-bit group prime every Cognito user pool uses; g is 2.
var srpN, _ = new(big.Int).SetString(srpNHex, 16)

var srpG = big.NewInt(2)

const srpNHex = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74" +
	"020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F1437" +
	"4FE1356D6D51C245E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3DC2007CB8A163BF05" +
	"98DA48361C55D39A69163FA8FD24CF5F83655D23DCA3AD961C62F356208552BB" +
	"9ED529077096966D670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B" +
	"E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9DE2BCBF695581718" +
	"3995497CEA956AE515D2261898FA051015728E5A8AAAC42DAD33170D04507A33" +
	"A85521ABDF1CBA64ECFB850458DBEF0A8AEA71575D060C7DB3970F85A6E1E4C7" +
	"ABF5AE8CDB0933D71E8C94E04A25619DCEE3D2261AD2EE6BF12FFA06D98A0864" +
	"D87602733EC86A64521F2B18177B200CBBE117577A615D6C770988C0BAD946E2" +
	"08E24FA074E5AB3143DB5BFCE0FD108E4B82D120A93AD2CAFFFFFFFFFFFFFFFF"

// srpPad is Cognito's hex padding: an even number of digits, and a leading
// 00 when the top bit is set (so the value reads as a positive integer).
func srpPad(h string) string {
	if len(h)%2 == 1 {
		return "0" + h
	}
	if strings.ContainsRune("89abcdefABCDEF", rune(h[0])) {
		return "00" + h
	}
	return h
}

// srpHashHex is SHA-256 over the bytes a hex string encodes, as hex.
func srpHashHex(h string) (string, error) {
	b, err := hex.DecodeString(h)
	if err != nil {
		return "", clierr.New("signup_failed", "the sign-in challenge was malformed")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func srpHashInt(h string) (*big.Int, error) {
	s, err := srpHashHex(h)
	if err != nil {
		return nil, err
	}
	n, _ := new(big.Int).SetString(s, 16)
	return n, nil
}

// srpA is the client's public value g^a mod N.
func srpA(a *big.Int) *big.Int { return new(big.Int).Exp(srpG, a, srpN) }

// srpSignature computes PASSWORD_CLAIM_SIGNATURE for a PASSWORD_VERIFIER
// challenge. poolName is the user pool id after the underscore.
func srpSignature(poolName, userID, password, saltHex, secretBlockB64, timestamp string, a, srvB *big.Int) (string, error) {
	if new(big.Int).Mod(srvB, srpN).Sign() == 0 {
		return "", clierr.New("signup_failed", "the sign-in challenge was invalid")
	}
	k, err := srpHashInt(srpPad(srpNHex) + srpPad(srpG.Text(16)))
	if err != nil {
		return "", err
	}
	u, err := srpHashInt(srpPad(srpA(a).Text(16)) + srpPad(srvB.Text(16)))
	if err != nil {
		return "", err
	}
	inner := sha256.Sum256([]byte(poolName + userID + ":" + password))
	x, err := srpHashInt(srpPad(saltHex) + hex.EncodeToString(inner[:]))
	if err != nil {
		return "", err
	}
	// S = (B - k*g^x) ^ (a + u*x) mod N
	base := new(big.Int).Exp(srpG, x, srpN)
	base.Mul(base, k).Sub(srvB, base).Mod(base, srpN)
	exp := new(big.Int).Mul(u, x)
	exp.Add(exp, a)
	S := base.Exp(base, exp, srpN)

	// HKDF-SHA256 with salt u, one 16-byte block, info "Caldera Derived Key".
	uBytes, _ := hex.DecodeString(srpPad(u.Text(16)))
	sBytes, _ := hex.DecodeString(srpPad(S.Text(16)))
	prk := hmacSHA256(uBytes, sBytes)
	key := hmacSHA256(prk, []byte("Caldera Derived Key\x01"))[:16]

	secretBlock, err := base64.StdEncoding.DecodeString(secretBlockB64)
	if err != nil {
		return "", clierr.New("signup_failed", "the sign-in challenge was malformed")
	}
	msg := append([]byte(poolName+userID), secretBlock...)
	msg = append(msg, timestamp...)
	return base64.StdEncoding.EncodeToString(hmacSHA256(key, msg)), nil
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

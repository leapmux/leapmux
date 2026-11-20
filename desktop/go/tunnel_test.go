package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tunnelpkg "github.com/leapmux/leapmux/tunnel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTunnelOwner is the expected user every tunneled create in these tests
// admits; the manager filters listings by it.
const testTunnelOwner = "user-a"

// testExpectedWorkerKeysHex is one exact composite Worker key set (x25519 ||
// mlkem || slhdsa, hex-encoded) generated for tests: the expectation a valid
// create carries. The desktop module cannot generate keys at runtime (the
// generator lives under the backend module's internal/ tree), so the set is a
// fixture -- a test key, never a real one.
const testExpectedWorkerKeysHex = "e3300b6b95c4556c9009c16f7fd919ce69476420df62b171d1a1c5d4949eca3be181c03a9a901cc22a9af4ab30f2757531a45bb99aa7eb52975298a0862c30643849987467bc848162658dc72c821ac61255360e2a2a3793c2591999e659b4f7e4a3929064ad01370035240d7c991327474aeb67ce363088446d264a3825bc0b00a2a8a6b19f83852fd715a333b7a84aa68f9a623bb36505af5850cb2a90f5a1af3281ab58888cb15c95d507bd9edba8e2d474f45b0c6b0264184ba3899abf3e54624662148541578608166b1b1635924ad1679acef343f8f78fdcda85ce2587b3dbbe616c6c8b2348416ac6ff45078de6867dd21184d17dc256c456062928b33c8d21cfbefbcb1ae713356a7dbf679a3f05be65db31d31bbcd7bb4e6703cc389531373acfc41537725b4fc91c47041b57de89105170889cac005085976f5343cba52c0a68be3626b4d126608b859d5f8c1fcf5b60276484e498829cd30856354a6ce89b88b7c1b62447f1f9bd69b5614156a8f4a624cd9b6ca1d4431d225172cac17e7787c5eb32b524abc8a13e87d0a27cf870cf167b64013f4e543ba040b8daea019714b9f8e8ae6d43cfa28b53a929107da58cde3c754c142b5e826008468c6e9b64b00aa3729b8440a87f7c57094b7bac69195402626fa0e3290be8114816606077250f907aa19359e31848a850ac25041e29820eaa078f25b12a7df886eec273db07c7016c239858b266c7b3c3f359a5a46db2e3784e302d15aa1021a51ec38b87d3450e58367ecc770b89947d86b7941e3b54de8c4072671aa5140f367b74fb4cbebfa7237524a9eec13c7e883517c8c64cd52a78ec82601c4597c74e68ab7b37aac03d63cc0a494c16abc3703ac7b486b1cd23b8d9406a824a99e2c4b185bc937884ca1fb2ccde929356969f1a60bff8f01fb3b65d1c5bb8ab0a4afcd67595d2b4cceb1b37436a71da469e96cb43558cfbe75e27ac1f65118aa5c24b415982d7d9717a87b3e572a25514931391b18f34a4331812c1a5a10e0a7e7dca46f86b8a815a6c160625c0d13fe50303f4a5ab667b8a87311d8561944988813bc7950e1baa4b77ceaf4a2430e692f99c1f5d53a57f4a768a3a0e2d56467ee435a2802a023889eb9b7282585c685a87bc635ebc038e1dcab033f6ac690744e73327019126b91b2e23763049465271100dbe07af839656e8d16ab3aa10e3d168b6513706e891983a864b21a2568c457bcc86761732ba62ce10c67e39c8c73a9897c0a07f2e41ce94a9cb8a5b1b48aa872a8a9d7ee76ab5b06186e335e3831d0d630384c96023155ff6c95b0f6ac874387976e15546d3ccb13653dd8c0584ca580b323f0ddb9e953846351304c17598f4c2baf2a621cafc28be09c70d38b8cbe4a0909490bd768d0cf254b3046d9eb60aed2381af268baeea9430545e7bc6ba36dac13c7237c1ab6a690777b3b503fc413fe36627c022b3e603ba14eba60fc48c266a8dfd280cd379c202e71897b7c27d10b36ab16fe6a97b71a7b29e8aa4bb06a72a37985b91324ee15c4362c5e1f7c60b111a96b4783d106fb1eb3cd3a4a5f11933eaa6b5239ba8ab9b4e1e524c0f081d764067e5bb57f543952ba087c02426b515296b639be68bb29df2a43d1b30b2c249dc822426662e2b8924971a8e6efac7b4d7a0adf592f4367de56a193dac20889a4dc7c91fee79394df8536eec608614b4c8f72b90845a719cb886059c6a5c368c3b7c00178e3b500756f9cf899c4af2b163a07b90d9217f86d0bd364953571184c7526cde768e35d8a574068856e5877852c84bb131a3028f61412dad732c2e84bf84e4943865a08042a904fba94887bdbfb9a1551789fa238021535954241649367ad6ccbe0b5a0e8ce86668a37e610b808b0ccb75d8c96c49b363113b2c96a11b651c15e77e114083e54c578fd02f8d68b675cc52cb498b2d153b4a08ad9ac606b6692c16402094002fb4d160bc447635585d946b279b617a037b9cf278b4eed784a92b6d5332ae0c2b0a8570bb105a20eac83408c597ae51796d647a461bc5b548c6e9876b0e12051260552cd154f6fb06d9ca5bcc5a3113f1a05582bf0e2201f5f9c70d29a8b95c1b88e7385f5bb8bd7a829c69c14490762be5c5dc495ed493605201bfac928fce168f2f44c9fba37cc7ec865506b802c61d1cd7c93901880c66c11af1bb2761827cd077d1ab21244969b0f4456476ab361e36d485e18dee424484f29099528d847ba6ba76960cd99942447b0e84eb2ca6d3bcb5271cf9bde9a59b5ef597f684a5d7fdb367d122cf85138909d8ce51d133d114bed931914fbd6a4904bc902f64e1d3aa25e1a997"

// testOtherWorkerKeysHex is a second exact composite Worker key set, valid but
// different from testExpectedWorkerKeysHex: the announcement a mismatched
// create or reconnect presents.
const testOtherWorkerKeysHex = "401212299ec2e5dd2ef64b17282d5771fbe150e91abb712317db32a68d5144447be972546a451626966034427c79c1c1d846681aaaf5c7b0f9a68913f5ce40376e07b02f9ee79232e69cc200844909880741a8c6d481150b70871067b648c1e620aec1c465f38636a9d73042925bb047263401cc01417ec8ec21a8561bc8b3534bc977799785abe98cc756313c31acf44b33dce9afa21616db336b0dabc7d8ba60f935a25a395435f73634c64659b952d7158c7b372fd2427c0e62b22e8566e184527a603bc8247946493d42c1807bbaca0eea2ff2fa83db33b1ec2202832402f23c7342a9380d583a7d82a49838988830c688f473f2f1744eb3963fb1bcb6c235b928c71e59539c739771a5be4548b66d1a05bf860b6d14ac2bb20fce158e5b53a07395be65885878e3cb8ce510de30a44b39552a7ac2bcf6ab86f45c8bec92ac57be05060cada0102d891e3eccc3235b21ecb01886a92e1a950e81f3bafa58299ec9474d0b5c3cb823dd9b875869b8693333262811c765ac838b41dde1367a099597ab20148baddc01696fa62aaab72292823ec5fa1f25d85e5c71b36480173e19b8f5380188c7a7fcf4c467b1a0d4a24bf276065c1ba33490810011b0758726904c348093611eec2686866a86484dbe56ae80b98781b5129565a86c3aa00ef24090a38eff3b4456b60eaf3112fa862c64f1a108dab6fe649b61e3c8758c4dc18812f01ab8a83241d96b5b9ce169d39214a663bcb14c8d92ea93bd1124072a7efe605faee7ca7bfc7e5296bfe937aa1eba7118e2cb38d42911aa5b75192ed8b612937091e1d29db863731946bbea067935f9b1762221551c0a86e80be18534b667ce0a31c0fa897858d0ceae1cc2bb9687aa4c4aac35cdba84686ce4a60fd90c78d7801488290171936611c99ffc808e9c14a5a3413383c7c5657696ea6c75c04ed6b45d837a62d77a2c8e12063059332d1b5142215e38d83b95b8c3c55b04885c887d8c018355483e84973aa29f22273eb71939c3f978a197645f57322b005c05f11065e27344e88245c0cc3031955ad72380d966560821abb9a267c549773b5cf063a41f51226f808ccc77658c68c666d362ff426d8459679ca9742d111d72cb30275c984485477cd71ec2107b79ab4a54a43629db68883ac5d79986b7acb068643dd7b1024523620f3507481ac91b32b6e4c9a92ad3260108b645379cae88c3debc3750bb17d0069472ac9ab927955e678d77b392c5fb44142a8b68ecc25f283dc3e779a52a3a268787fd763680d08fba6a434e067c0a44981d935c77fc87240661d6a75a8b4ac00332c0c110cffcaa2ca7648a4f6b61f9fb9450db315ac62434a342b5ac28d88509ec289f3643391fc5b732c3b468caae9722b5a23ab2ebd02895390c0837b39a411e54e670c831ad17648eec179b4b5623e117ce0a628b202024c8b94e05fb985d93bb645a8f44b8a22512762a356277f70144db66659b1385c64f5e558e0b0793580820f05509b5410e3641a2dde5615871a563ca359223aa5b0375cc097fc7c87f9ef91230c44c2e68c7e52285652c53f4644f78a51bee6518e8f3350e930b43fb6ab0e58706169b7a1b2929934908ec756233cce0b47c2a439e1634c5c34c3a7f8bbb1cf177f9b340d5c65be227ada8a2caadc5caa556bdaf687f18c40424f3775e6902373b309bbc6a7c32787187c99b8bb1f4f4b9842682d82a949656bceb69c2043625831554123271f157084587692c69a22475305301933da0a3ec61cceca14c3db3c9af8a7a49b18f9ac72960301b96989fce725808330133631ced31acec3889718bb8fca169416a41f9e99982680330864c4109a9daf1b8cb9201bd93ce80052bbc176512c02185727047e549d5c22e414a8a535093c9525bd3a48b7e5a056de51eba65bf46fc6c437a7892268c55e00d20a9576e951aa3683c32ea48b4aac3d79c144a016781da86198a6c9f17bbe6e213c205bd9ed50839719f32368e46530b02d74a91d59cb2c74b01908afb81a3746c9a49a6005b29942b858e80b35e495c0b3271439126977b93439eb81dfaaa793a983adea03cf2cc53538b9ad578546f0035fff98afa662ab6704e8d782cbf4830a858659b6ac8c793622ee9b72bc96573205899a3bead8ab878d05c3bc778595662eb4296b699a04fab12d6609ca4958159d48c2fa28243db8e5d451067a5ae4f644212820929a5de493627bccc4b55071ca76f627d3f4a14cbec0eb6127ccb83a5ba28a8ef836bcc69f55874d915a10f71a2cec89d66a656ed731c31f6c059a675e04e86250a5e5d5f589dd8d8b9473790741e5a4977b84443c86c44e5ba76"

// listTunnelRows returns the rows ListTunnels reports for user, asserting the
// listing succeeds.
func listTunnelRows(t *testing.T, m *TunnelManager, user string) []TunnelInfo {
	t.Helper()
	_, rows, err := m.ListTunnels(user)
	require.NoError(t, err)
	return rows
}

// testChannelEpoch stands in for the channel epoch a valid create carries.
// A create admitted through TunnelManager.CreateTunnel must present the
// MANAGER's current epoch instead; see the call sites that stamp
// cfg.ChannelEpoch = m.currentEpoch().
const testChannelEpoch = "epoch-test"

// withTestExpectation fills the admission fields a valid create carries: the
// Worker identity and a channel epoch.
func withTestExpectation(cfg TunnelConfig) TunnelConfig {
	cfg.ExpectedUserID = testTunnelOwner
	cfg.ExpectedWorkerKeysHex = testExpectedWorkerKeysHex
	cfg.ChannelEpoch = testChannelEpoch
	return cfg
}

func TestTunnelManager_CreatePortForward_Validation(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx := context.Background()

	t.Run("empty workerId", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 80})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "workerId is required")
	})

	t.Run("nil operation context", func(t *testing.T) {
		_, err := m.CreateTunnel(nil, ctx, TunnelConfig{}) //nolint:staticcheck // Exercise the public boundary.
		require.EqualError(t, err, "operation context is required")
	})

	t.Run("nil lifetime context", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, nil, TunnelConfig{})
		require.EqualError(t, err, "lifetime context is required")
	})

	t.Run("invalid type", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: "invalid"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "type must be")
	})

	t.Run("port_forward missing targetAddr", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetPort: 80})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetAddr is required")
	})

	t.Run("port_forward invalid targetPort 0", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 0})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetPort must be")
	})

	t.Run("port_forward invalid targetPort 70000", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 70000})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "targetPort must be")
	})

	t.Run("invalid bindPort", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "127.0.0.1", TargetPort: 80, BindPort: 70000})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bindPort must be")
	})

	t.Run("missing expected user", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{
			WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1",
			ExpectedWorkerKeysHex: testExpectedWorkerKeysHex,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expectedUserId is required")
	})

	t.Run("missing expected keys", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{
			WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1",
			ExpectedUserID: testTunnelOwner,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expectedWorkerKeysHex is required")
	})

	t.Run("unusable expected keys", func(t *testing.T) {
		_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{
			WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1",
			ExpectedUserID:        testTunnelOwner,
			ExpectedWorkerKeysHex: "deadbeef",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the expected worker keys")
	})
}

func TestTunnelManager_ListAndDelete(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	// Initially empty.
	assert.Empty(t, listTunnelRows(t, m, testTunnelOwner))

	// We can't create a real tunnel without a channel, but we can test
	// the Delete path with a manually inserted tunnel.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	m.mu.Lock()
	m.tunnels["test-t1"] = &tunnel{
		info: TunnelInfo{
			ID:         "test-t1",
			WorkerID:   "w1",
			Type:       "port_forward",
			BindAddr:   "127.0.0.1",
			BindPort:   ln.Addr().(*net.TCPAddr).Port,
			TargetAddr: "10.0.0.1",
			TargetPort: 8080,
		},
		expect:      channelExpectation{userID: testTunnelOwner},
		listener:    ln,
		cancel:      cancel,
	}
	m.mu.Unlock()

	// List should return the tunnel.
	tunnels := listTunnelRows(t, m, testTunnelOwner)
	require.Len(t, tunnels, 1)
	assert.Equal(t, "test-t1", tunnels[0].ID)
	assert.Equal(t, tunnelTypePortForward, tunnels[0].Type)

	// Delete should succeed.
	err = m.DeleteTunnel("test-t1", m.currentEpoch())
	require.NoError(t, err)
	assert.Empty(t, listTunnelRows(t, m, testTunnelOwner))

	// Verify listener is closed.
	_, err = ln.Accept()
	assert.Error(t, err, "listener should be closed after delete")
	_ = ctx // keep linter happy
}

// scriptedListener returns a scripted sequence of Accept results, so a test can
// drive the accept loop's error policy without provoking real fd exhaustion.
//
// `results` is consumed first, one error per Accept. Once it is exhausted, any
// conns in `conns` are handed out in order -- letting a test assert that an Accept
// FOLLOWING a transient failure still delivers. After both are spent, Accept blocks
// until Close so the loop parks instead of spinning.
type scriptedListener struct {
	mu        sync.Mutex
	results   []error
	conns     []net.Conn
	calls     int
	closed    chan struct{}
	closeOnce sync.Once
}

func newScriptedListener(results ...error) *scriptedListener {
	return &scriptedListener{results: results, closed: make(chan struct{})}
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	i := l.calls
	l.calls++
	var err error
	if i < len(l.results) {
		err = l.results[i]
	}
	var conn net.Conn
	if err == nil && len(l.conns) > 0 {
		conn, l.conns = l.conns[0], l.conns[1:]
	}
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if conn != nil {
		return conn, nil
	}
	// Script exhausted: block until closed so the loop parks instead of spinning.
	<-l.closed
	return nil, net.ErrClosed
}

// Close is idempotent and safe to call CONCURRENTLY, which a select/default
// check-then-act only looks like: two goroutines can both find the channel open
// and both close it, panicking with "close of closed channel". That is reachable
// here -- acceptBeforeIdleDeadline closes the listener from both its
// idle-deadline and its shutdown arm, and a test's own cleanup races either --
// so the retry test surfaced it as a load-dependent flake rather than a
// deterministic failure. Not guarded by mu, because Accept parks on `closed`
// and would deadlock against a Close that needed the same lock.
func (l *scriptedListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}
func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero} }

// TestScriptedListenerCloseIsConcurrencySafe pins the contract socket.go relies
// on. acceptBeforeIdleDeadline closes the listener from both its idle-deadline
// and its shutdown arm, and a caller's own cleanup can race either, so Close has
// to survive concurrent callers -- not merely repeated ones.
//
// The distinction is the whole point: a select/default check-then-act IS
// idempotent when called in sequence and still panics with "close of closed
// channel" when two goroutines both find the channel open. That is why the bug
// reached a full-suite run before it was seen, as a load-dependent failure of
// the retry test rather than of anything that looked like this.
func TestScriptedListenerCloseIsConcurrencySafe(t *testing.T) {
	t.Parallel()
	for range 200 {
		l := newScriptedListener()
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, l.Close())
			}()
		}
		wg.Wait()
		assert.ErrorIs(t, func() error { _, err := l.Accept(); return err }(), net.ErrClosed,
			"a closed listener must keep reporting itself closed")
	}
}

func (l *scriptedListener) acceptCalls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// installTestTunnel registers a tunnel backed by ln so the accept loop can be
// driven directly.
func installTestTunnel(t *testing.T, m *TunnelManager, id string, ln net.Listener) (*tunnel, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tn := &tunnel{
		info:        TunnelInfo{ID: id, WorkerID: "w1", Type: tunnelTypePortForward},
		expect:      channelExpectation{userID: testTunnelOwner},
		listener:    ln,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[id] = tn
	m.mu.Unlock()
	return tn, ctx
}

// A TRANSIENT accept failure must not kill the tunnel. EMFILE is fd exhaustion --
// exactly what a browser fanning out through SOCKS5 or a `git fetch` over many
// port-forward conns provokes -- and it clears on its own in milliseconds.
// Treating it as fatal left the tunnel registered and reported healthy while it
// accepted nothing ever again, so every new local connection failed with no
// visible cause.
func TestTunnelManager_AcceptRetriesTransientError(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	ln := newScriptedListener(transientAcceptErr(), transientAcceptErr())
	tn, ctx := installTestTunnel(t, m, "t-transient", ln)

	done := make(chan struct{})
	go func() { defer close(done); m.acceptPortForward(ctx, tn, nil) }()

	// The loop must get past both transient failures and still be serving.
	require.Eventually(t, func() bool { return ln.acceptCalls() > 2 },
		2*time.Second, 10*time.Millisecond,
		"a transient accept error must be retried, not fatal")
	assert.Len(t, listTunnelRows(t, m, testTunnelOwner), 1, "the tunnel survives a transient accept error")

	tn.cancel()
	_ = ln.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop did not exit on cancel")
	}
}

// The SOCKS5 path must ride out a transient accept error too.
//
// It does not run acceptPortForward's loop: it hands its listener to go-socks5's
// Server.Serve, which returns on ANY Accept error and has no retry of its own, and
// serveSocks5 then fails the tunnel. So while the retry policy lived only in
// acceptPortForward, an EMFILE spike -- the "browser fanning out through SOCKS5"
// workload isTemporaryAcceptError explicitly cites -- was ridden out by a
// port-forward tunnel but permanently deleted a SOCKS5 one. tunnelListener.Accept
// is the shared seam both loops now retry through, so this drives it directly.
func TestTunnelListenerAcceptRetriesTransientError(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	// Two transient failures, then a real connection: Serve must never see them.
	ln := newScriptedListener(transientAcceptErr(), transientAcceptErr())
	ln.conns = []net.Conn{server}
	tn, ctx := installTestTunnel(t, m, "t-socks-transient", ln)

	accepted := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := tunnelListener{Listener: ln, tunnel: tn, ctx: ctx}.Accept()
		if err != nil {
			errCh <- err
			return
		}
		accepted <- conn
	}()

	select {
	case conn := <-accepted:
		assert.NotNil(t, conn, "the connection after the transient failures is delivered")
		assert.Greater(t, ln.acceptCalls(), 2, "both EMFILEs were retried, not surfaced")
	case err := <-errCh:
		t.Fatalf("a transient accept error must be retried, not surfaced to Serve: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Accept never returned the connection that followed the transient failures")
	}
}

// A PERMANENT accept failure must still surface through tunnelListener, so
// serveSocks5 fails the tunnel rather than spinning forever.
func TestTunnelListenerAcceptSurfacesPermanentError(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	ln := newScriptedListener(errors.New("listener is broken"))
	tn, ctx := installTestTunnel(t, m, "t-socks-fatal", ln)

	_, err := tunnelListener{Listener: ln, tunnel: tn, ctx: ctx}.Accept()
	require.Error(t, err, "a permanent accept error must reach Serve so the tunnel is failed")
	assert.Contains(t, err.Error(), "listener is broken")
}

// A PERMANENT accept failure must remove the tunnel. The accept loop is gone for
// good, so leaving it registered kept ListTunnels (and the UI) reporting a tunnel
// that could never accept again -- a silent death.
func TestTunnelManager_AcceptFailureRemovesTunnel(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	ln := newScriptedListener(errors.New("listener is broken"))
	tn, ctx := installTestTunnel(t, m, "t-fatal", ln)
	require.Len(t, listTunnelRows(t, m, testTunnelOwner), 1)

	m.acceptPortForward(ctx, tn, nil)

	assert.Empty(t, listTunnelRows(t, m, testTunnelOwner),
		"a tunnel whose listener died must not keep reporting healthy")
}

func TestTunnelManager_DeleteNonExistent(t *testing.T) {
	m := NewTunnelManager()
	err := m.DeleteTunnel("nonexistent", m.currentEpoch())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tunnel not found")
}

// TestTunnelManager_StaleChannelEpochRefusesCreateAndDelete pins the fencing
// the request's channel_epoch performs: a mutation fenced on a listing a reset
// has since invalidated must be refused before it touches state, so a stale
// caller re-reads instead of mutating under the successor epoch.
func TestTunnelManager_StaleChannelEpochRefusesCreateAndDelete(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	retired := m.currentEpoch()
	m.CloseAll() // rotates the epoch; every listing of `retired` is now stale
	require.NotEqual(t, retired, m.currentEpoch())

	cfg := withTestExpectation(TunnelConfig{
		HubURL: "http://hub", WorkerID: "worker",
		Type: tunnelTypePortForward, TargetAddr: "target", TargetPort: 80,
		BindAddr: "127.0.0.1", BindPort: 18080,
	})
	cfg.ChannelEpoch = retired
	_, err := m.CreateTunnel(context.Background(), context.Background(), cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale channel epoch")

	err = m.DeleteTunnel("any-tunnel", retired)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale channel epoch")
}

func TestTunnelManager_CloseAll(t *testing.T) {
	m := NewTunnelManager()

	// Insert 3 fake tunnels with real listeners.
	for i := 0; i < 3; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		_, cancel := context.WithCancel(context.Background())
		id := "t-" + string(rune('a'+i))
		m.mu.Lock()
		m.tunnels[id] = &tunnel{
			info:     TunnelInfo{ID: id, WorkerID: "w1", Type: tunnelTypePortForward},
			expect:   channelExpectation{userID: testTunnelOwner},
			listener: ln,
			cancel:   cancel,
		}
		m.mu.Unlock()
	}

	assert.Len(t, listTunnelRows(t, m, testTunnelOwner), 3)

	m.CloseAll()

	assert.Empty(t, listTunnelRows(t, m, testTunnelOwner))
}

func TestTunnelManager_CloseAllFencesInflightTunnelCreation(t *testing.T) {
	m := NewTunnelManager()
	started := make(chan struct{})
	release := make(chan struct{})
	channel, channelClosed := newTestManagedChannel()
	m.pool.openCh = func(
		_ context.Context,
		_, _ string,
		_ *tunnelpkg.OpenChannelOptions,
	) (*managedChannel, error) {
		close(started)
		<-release
		return channel, nil
	}

	result := make(chan error, 1)
	cfg := withTestExpectation(TunnelConfig{
		HubURL: "http://hub", WorkerID: "worker",
		Type: tunnelTypePortForward, TargetAddr: "target", TargetPort: 80,
		BindAddr: "127.0.0.1", BindPort: 18080,
	})
	cfg.ChannelEpoch = m.currentEpoch()
	go func() {
		_, err := m.CreateTunnel(context.Background(), context.Background(), cfg)
		result <- err
	}()
	<-started
	m.CloseAll()
	close(release)

	require.Error(t, <-result)
	assert.True(t, channelClosed.Load(), "stale channel was not closed")
	m.mu.Lock()
	assert.Empty(t, m.tunnels)
	m.mu.Unlock()
	m.pool.mu.Lock()
	assert.Empty(t, m.pool.channels)
	m.pool.mu.Unlock()
}

func TestTunnelManager_TeardownClosesPortForwardConnections(t *testing.T) {
	tests := []struct {
		name  string
		close func(*TunnelManager, string) error
	}{
		{
			name: "DeleteTunnel",
			close: func(m *TunnelManager, id string) error {
				return m.DeleteTunnel(id, m.currentEpoch())
			},
		},
		{
			name: "CloseAll",
			close: func(m *TunnelManager, _ string) error {
				m.CloseAll()
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := NewTunnelManager()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			tun := &tunnel{
				info: TunnelInfo{
					ID: "tunnel", WorkerID: "worker", Type: tunnelTypePortForward,
					TargetAddr: "target", TargetPort: 80,
				},
				listener: listener,
				cancel:   cancel,
				// Match CreateTunnel: handlePortForward tracks each owned conn in
				// this map (addConnection), so a live tunnel must have it non-nil.
				connections: make(map[net.Conn]struct{}),
			}
			m.tunnels[tun.info.ID] = tun
			target, targetPeer := net.Pipe()
			t.Cleanup(func() {
				_ = target.Close()
				_ = targetPeer.Close()
			})
			dialed := make(chan struct{})
			m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
				close(dialed)
				return target, nil
			}
			go m.acceptPortForward(ctx, tun, nil)

			client, err := net.Dial("tcp", listener.Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = client.Close() })
			<-dialed
			require.NoError(t, test.close(m, tun.info.ID))

			assertConnectionClosed(t, client, "accepted client connection remained open")
			assertConnectionClosed(t, targetPeer, "remote tunnel connection remained open")
		})
	}
}

func TestTunnelManager_DeleteCancelsInFlightPortForwardDial(t *testing.T) {
	m := NewTunnelManager()
	ctx, cancel := context.WithCancel(context.Background())
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "in-flight", WorkerID: "worker", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
		},
		cancel:      cancel,
		connections: map[net.Conn]struct{}{client: {}},
	}
	m.tunnels[tun.info.ID] = tun
	dialStarted := make(chan struct{})
	m.dial = func(ctx context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		close(dialStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	handlerDone := make(chan struct{})
	go func() {
		m.handlePortForward(ctx, client, tun, nil)
		close(handlerDone)
	}()
	<-dialStarted
	require.NoError(t, m.DeleteTunnel(tun.info.ID, m.currentEpoch()))

	completed := false
	select {
	case <-handlerDone:
		completed = true
	case <-time.After(200 * time.Millisecond):
	}
	assert.True(t, completed, "tunnel deletion left the remote dial running")
	assert.ErrorIs(t, ctx.Err(), context.Canceled)
}

type testAddrConn struct {
	net.Conn
	localAddr net.Addr
}

func (c testAddrConn) LocalAddr() net.Addr { return c.localAddr }

func TestTunnelManager_DeleteClosesSocks5Connections(t *testing.T) {
	m := NewTunnelManager()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	tun := &tunnel{
		info:     TunnelInfo{ID: "socks", WorkerID: "worker", Type: tunnelTypeSocks5},
		listener: listener,
		cancel:   cancel,
		// Match CreateTunnel: serveSocks5 owns the accepted client and dialed
		// target via addConnection, which writes this map -- it must be non-nil.
		connections: make(map[net.Conn]struct{}),
	}
	m.tunnels[tun.info.ID] = tun
	target, targetPeer := net.Pipe()
	t.Cleanup(func() {
		_ = target.Close()
		_ = targetPeer.Close()
	})
	dialed := make(chan struct{})
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		close(dialed)
		return testAddrConn{
			Conn:      target,
			localAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		}, nil
	}
	go m.serveSocks5(ctx, tun, nil)

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.SetDeadline(time.Now().Add(time.Second)))
	_, err = client.Write([]byte{5, 1, 0})
	require.NoError(t, err)
	greeting := make([]byte, 2)
	_, err = io.ReadFull(client, greeting)
	require.NoError(t, err)
	require.Equal(t, []byte{5, 0}, greeting)
	_, err = client.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80})
	require.NoError(t, err)
	reply := make([]byte, 10)
	_, err = io.ReadFull(client, reply)
	require.NoError(t, err)
	require.Zero(t, reply[1], "SOCKS5 CONNECT failed")
	<-dialed
	require.NoError(t, client.SetDeadline(time.Time{}))

	require.NoError(t, m.DeleteTunnel(tun.info.ID, m.currentEpoch()))
	assertConnectionClosed(t, client, "SOCKS5 client connection remained open")
	assertConnectionClosed(t, targetPeer, "SOCKS5 remote tunnel connection remained open")
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func assertConnectionClosed(t *testing.T, conn net.Conn, message string) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		assert.False(t, isTimeoutError(err), message)
		return
	}
	_, err := conn.Read(make([]byte, 1))
	require.Error(t, err)
	assert.False(t, isTimeoutError(err), message)
}

func TestOwnedTunnelConnForwardsCloseWriteForSocks5(t *testing.T) {
	// go-socks5 asserts `interface { CloseWrite() error }` on the client
	// connection after the remote->client copy to signal read-EOF. Wrapping the
	// accepted *net.TCPConn in ownedTunnelConn must not hide CloseWrite from
	// that assertion (regression: tunnelListener.Accept returns the wrapper).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := ln.Accept()
		if acceptErr != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	defer func() { _ = client.Close() }()

	server := <-accepted
	require.NotNil(t, server)
	tcpConn := server.(*net.TCPConn)
	defer func() { _ = tcpConn.Close() }()

	owned := &ownedTunnelConn{Conn: tcpConn, tunnel: &tunnel{connections: make(map[net.Conn]struct{})}}

	// Compile-time: satisfies go-socks5's closeWriter interface.
	var closeWriter interface{ CloseWrite() error } = owned

	peerSawEOF := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, client) // returns once the server half-closes
		close(peerSawEOF)
	}()

	require.NoError(t, closeWriter.CloseWrite())
	select {
	case <-peerSawEOF:
	case <-time.After(time.Second):
		t.Fatal("CloseWrite did not half-close the underlying TCP conn (peer saw no EOF)")
	}
}

// fakeChannel stands in for *tunnelpkg.Channel: the manager never opens a real one
// under test, which would need a hub, a WebSocket, and a Noise handshake. It mirrors
// the contract the manager depends on -- Close is idempotent and ends the lifetime,
// and Closed reports both an explicit close and a lifetime cancelled from elsewhere,
// exactly as tunnel.Channel does.
type fakeChannel struct {
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
}

func (f *fakeChannel) Closed() bool { return f.closed.Load() || f.ctx.Err() != nil }

func (f *fakeChannel) Close() {
	if f.closed.CompareAndSwap(false, true) {
		f.cancel()
	}
}

func (f *fakeChannel) Context() context.Context { return f.ctx }

// newTestManagedChannel returns a cached-channel double and the flag reporting
// whether it has been closed. Its concrete channel is nil: every test using it also
// overrides TunnelManager.dial, which is the only thing that reads it.
func newTestManagedChannel() (*managedChannel, *atomic.Bool) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := &fakeChannel{ctx: ctx, cancel: cancel}
	return &managedChannel{handle: fake}, &fake.closed
}

func TestTunnelManager_DefaultBindPort_PortForward(t *testing.T) {
	// Verify that CreateTunnel uses TargetPort as default BindPort.
	// We can't fully create (no channel), but we can check validation passes.
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := withTestExpectation(TunnelConfig{
		WorkerID:   "w1",
		Type:       "port_forward",
		TargetAddr: "10.0.0.1",
		TargetPort: 3000,
		BindAddr:   "127.0.0.1",
		// BindPort: 0 — should default to 3000
	})
	cfg.ChannelEpoch = m.currentEpoch()

	// This will fail at getOrOpenChannel (no Hub), but the validation should pass.
	ctx := context.Background()
	_, err := m.CreateTunnel(ctx, ctx, cfg)
	require.Error(t, err)
	// The error should be about channel, not about validation.
	assert.Contains(t, err.Error(), "open channel")
}

func TestTunnelManager_DefaultBindPort_Socks5(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := withTestExpectation(TunnelConfig{
		WorkerID: "w1",
		Type:     tunnelTypeSocks5,
		BindAddr: "127.0.0.1",
		// BindPort: 0 — should default to 1080
	})
	cfg.ChannelEpoch = m.currentEpoch()

	ctx := context.Background()
	_, err := m.CreateTunnel(ctx, ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel")
}

func TestTunnelManager_DefaultBindAddr(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()

	cfg := withTestExpectation(TunnelConfig{
		WorkerID:   "w1",
		Type:       "port_forward",
		TargetAddr: "10.0.0.1",
		TargetPort: 3000,
		// BindAddr: "" — should default to 127.0.0.1
	})
	cfg.ChannelEpoch = m.currentEpoch()

	ctx := context.Background()
	_, err := m.CreateTunnel(ctx, ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open channel")
}

// TestTunnelConfig_ValidateAndNormalize asserts the config defaulting and
// validation rules DIRECTLY, without binding a socket or opening a channel --
// the seam CreateTunnel's extracted validateAndNormalize provides. The
// CreateTunnel-level tests above can only assert defaulting indirectly (they
// infer it from reaching the "open channel" failure); here the normalized values
// and the loopback-only invariant are checked at their source.
func TestTunnelConfig_ValidateAndNormalize(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cases := []struct {
			name         string
			cfg          TunnelConfig
			wantBindAddr string
			wantBindPort int
		}{
			{
				name:         "port_forward defaults bind port to target port",
				cfg:          withTestExpectation(TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "10.0.0.1", TargetPort: 3000, BindAddr: "127.0.0.1"}),
				wantBindAddr: "127.0.0.1",
				wantBindPort: 3000,
			},
			{
				name:         "socks5 defaults bind port to 1080",
				cfg:          withTestExpectation(TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1"}),
				wantBindAddr: "127.0.0.1",
				wantBindPort: 1080,
			},
			{
				name:         "empty bind addr defaults to loopback",
				cfg:          withTestExpectation(TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "10.0.0.1", TargetPort: 3000}),
				wantBindAddr: defaultTunnelBindAddr,
				wantBindPort: 3000,
			},
			{
				name:         "explicit bind port is preserved",
				cfg:          withTestExpectation(TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1", BindPort: 9999}),
				wantBindAddr: "127.0.0.1",
				wantBindPort: 9999,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := tc.cfg
				require.NoError(t, cfg.validateAndNormalize())
				assert.Equal(t, tc.wantBindAddr, cfg.BindAddr)
				assert.Equal(t, tc.wantBindPort, cfg.BindPort)
			})
		}
	})

	t.Run("rejections", func(t *testing.T) {
		cases := []struct {
			name    string
			cfg     TunnelConfig
			wantErr string
		}{
			{"missing worker id", TunnelConfig{Type: tunnelTypeSocks5, BindAddr: "127.0.0.1"}, "workerId is required"},
			{"bad type", TunnelConfig{WorkerID: "w1", Type: "bogus", BindAddr: "127.0.0.1"}, "type must be"},
			{"port_forward missing target addr", TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetPort: 3000, BindAddr: "127.0.0.1"}, "targetAddr is required"},
			{"port_forward target port out of range", TunnelConfig{WorkerID: "w1", Type: tunnelTypePortForward, TargetAddr: "10.0.0.1", TargetPort: 70000, BindAddr: "127.0.0.1"}, "targetPort must be 1-65535"},
			{"non-loopback bind addr", TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "0.0.0.0"}, "loopback"},
			{"bind port out of range", TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1", BindPort: 70000}, "bindPort must be 1-65535"},
			{"missing channel epoch", TunnelConfig{WorkerID: "w1", Type: tunnelTypeSocks5, BindAddr: "127.0.0.1", ExpectedUserID: testTunnelOwner, ExpectedWorkerKeysHex: testExpectedWorkerKeysHex}, "channelEpoch is required"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				cfg := tc.cfg
				err := cfg.validateAndNormalize()
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
			})
		}
	})
}

func TestPortForwardDeliversResponseAfterClientHalfClose(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "pf", WorkerID: "w", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
			BindAddr: "127.0.0.1", BindPort: listener.Addr().(*net.TCPAddr).Port,
		},
		hubURL:      "http://hub",
		listener:    listener,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	// Remote target: read the full request (until the client's write half-close
	// arrives), then send a response.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetLn.Close() })
	go func() {
		c, acceptErr := targetLn.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.ReadAll(c) // drains once the forwarded client half-close arrives
		_, _ = c.Write([]byte("response"))
	}()
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		return net.Dial("tcp", targetLn.Addr().String())
	}
	go m.acceptPortForward(ctx, tun, nil)

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	_, err = client.Write([]byte("request"))
	require.NoError(t, err)
	// Half-close the write side, like an HTTP/1.0 request. The old code
	// full-closed the client's read side at this point, truncating "response".
	require.NoError(t, client.(*net.TCPConn).CloseWrite())

	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	resp, err := io.ReadAll(client)
	require.NoError(t, err)
	assert.Equal(t, "response", string(resp), "response truncated after client half-close")
}

// TestPortForwardReleasesConnWhenClientAborts pins the bound on the half-close's
// abort path: a client that vanishes with an RST leaves the remote->local copy
// parked in Read with nothing to wake it (no read deadline on this path, and ctx
// is the whole tunnel's lifetime), so handlePortForward blocked on its second
// <-copyDone forever, pinning the goroutine, both fds, the tunneled Conn's
// send-window slot and its worker-side conn-map entry until tunnel teardown.
//
// The sibling TestPortForwardDeliversResponseAfterClientHalfClose is the other
// half of the invariant: a CLEAN half-close must still linger for the response.
func TestPortForwardReleasesConnWhenClientAborts(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "pf-abort", WorkerID: "w", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
			BindAddr: "127.0.0.1", BindPort: listener.Addr().(*net.TCPAddr).Port,
		},
		hubURL:      "http://hub",
		listener:    listener,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	// A target that accepts and then goes silent: it never reads, writes, or
	// closes, so only the client's abort can end the copy.
	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = targetLn.Close() })
	targetAccepted := make(chan net.Conn, 1)
	go func() {
		c, acceptErr := targetLn.Accept()
		if acceptErr != nil {
			return
		}
		targetAccepted <- c
	}()
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		return net.Dial("tcp", targetLn.Addr().String())
	}

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	local, err := listener.Accept()
	require.NoError(t, err)

	handled := make(chan struct{})
	go func() {
		defer close(handled)
		m.handlePortForward(ctx, local, tun, nil)
	}()

	var targetConn net.Conn
	select {
	case targetConn = <-targetAccepted:
	case <-time.After(2 * time.Second):
		t.Fatal("port forward never dialled the target")
	}
	t.Cleanup(func() { _ = targetConn.Close() })

	_, err = client.Write([]byte("request"))
	require.NoError(t, err)
	// SetLinger(0) + Close is an RST, not a FIN: the copy ends in an error rather
	// than a clean EOF, so there is no response left to wait for.
	require.NoError(t, client.(*net.TCPConn).SetLinger(0))
	require.NoError(t, client.Close())

	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handlePortForward did not release an aborted connection")
	}
	tun.mu.Lock()
	remaining := len(tun.connections)
	tun.mu.Unlock()
	assert.Zero(t, remaining, "both ends must be released back to the tunnel")
}

// TestPortForwardReleasesConnsOnLifetimeCancel pins the context.AfterFunc arm of
// handlePortForward (issue #280): cancelling the parent lifetime ctx — without
// tunnel teardown (t.close / DeleteTunnel) and without a client abort — is the
// only path that closes both conn ends. The AfterFunc callback force-closes
// local and remote, unblocking the parked copyHalf goroutines so the handler
// returns and clears tun.connections. Teardown tests go through t.close(),
// which hard-closes tracked conns independently of the ctx, so they do not
// exercise this arm.
func TestPortForwardReleasesConnsOnLifetimeCancel(t *testing.T) {
	m := NewTunnelManager()
	defer m.CloseAll()
	ctx, cancel := context.WithCancel(context.Background())
	// Do not register cancel on t.Cleanup: the test cancels explicitly as the
	// trigger under test. A deferred CloseAll still tears the manager down.

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	tun := &tunnel{
		info: TunnelInfo{
			ID: "pf-lifetime", WorkerID: "w", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
			BindAddr: "127.0.0.1", BindPort: listener.Addr().(*net.TCPAddr).Port,
		},
		hubURL:      "http://hub",
		listener:    listener,
		cancel:      cancel,
		connections: make(map[net.Conn]struct{}),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	target, targetPeer := net.Pipe()
	t.Cleanup(func() {
		_ = target.Close()
		_ = targetPeer.Close()
	})
	dialed := make(chan struct{})
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		close(dialed)
		return target, nil
	}

	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	local, err := listener.Accept()
	require.NoError(t, err)

	handled := make(chan struct{})
	go func() {
		defer close(handled)
		m.handlePortForward(ctx, local, tun, nil)
	}()

	select {
	case <-dialed:
	case <-time.After(2 * time.Second):
		t.Fatal("port forward never dialled the target")
	}

	// Cancel only the lifetime ctx — no t.close(), DeleteTunnel, or client abort.
	// Both copyHalf directions are parked (nothing written); AfterFunc must
	// force-close both ends so the handler returns.
	cancel()

	select {
	case <-handled:
	case <-time.After(3 * time.Second):
		t.Fatal("handlePortForward did not release on lifetime cancel")
	}
	assertConnectionClosed(t, client, "client connection remained open after lifetime cancel")
	assertConnectionClosed(t, targetPeer, "remote tunnel connection remained open after lifetime cancel")
	tun.mu.Lock()
	remaining := len(tun.connections)
	tun.mu.Unlock()
	assert.Zero(t, remaining, "both ends must be released back to the tunnel")
}

// gatedTunnelListener lets a test place an accepted conn into the serve loop at a
// chosen instant: the first Accept parks until the gate opens (or the serve ctx
// dies), later ones park until the serve ctx dies. Only the Serve goroutine calls
// Accept, so handedOff needs no lock.
type gatedTunnelListener struct {
	net.Listener
	gate      <-chan struct{}
	conn      net.Conn
	done      <-chan struct{}
	handedOff bool
}

func (l *gatedTunnelListener) Accept() (net.Conn, error) {
	if !l.handedOff {
		l.handedOff = true
		select {
		case <-l.gate:
			return l.conn, nil
		case <-l.done:
			return nil, net.ErrClosed
		}
	}
	<-l.done
	return nil, net.ErrClosed
}

// t.close() must cancel the tunnel context BEFORE it marks the tunnel closed.
// The two orders are indistinguishable to close()'s own callers, but not to the
// serve loops: `closed` is what makes ownConnection reject an accepted conn, and
// ctx.Err() is what tells serveSocks5 that rejection was a teardown rather than a
// dead listener. Cancelling last leaves a window where the first is true and the
// second is not.
func TestTunnelCloseCancelsBeforeMarkingClosed(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	tun := &tunnel{connections: make(map[net.Conn]struct{})}
	closedAtCancel := true
	tun.cancel = func() {
		tun.mu.Lock()
		closedAtCancel = tun.closed
		tun.mu.Unlock()
		cancel()
	}

	tun.close()
	assert.False(t, closedAtCancel,
		"close() flipped closed before cancelling: the serve loop can see a rejected conn while ctx.Err() is still nil")
}

// The consequence of that ordering, end to end: an ordinary user-initiated
// DeleteTunnel must not be reported as a listener failure. In the old order a conn
// accepted in the window failed ownConnection, go-socks5's Serve returned
// net.ErrClosed, and serveSocks5 -- reading a ctx that was not cancelled yet --
// logged ERROR "tunnel listener failed; removing the tunnel", sending operators
// hunting a failure that never happened.
func TestServeSocks5DoesNotFailTunnelOnClose(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)
	ctx, cancelServe := context.WithCancel(context.Background())
	t.Cleanup(cancelServe)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.Dial("tcp", listener.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	accepted, err := listener.Accept()
	require.NoError(t, err)
	t.Cleanup(func() { _ = accepted.Close() })

	gate := make(chan struct{})
	gated := &gatedTunnelListener{Listener: listener, gate: gate, conn: accepted, done: ctx.Done()}
	tun := &tunnel{
		info:        TunnelInfo{ID: "socks-close", WorkerID: "w", Type: tunnelTypeSocks5},
		listener:    gated,
		connections: make(map[net.Conn]struct{}),
	}
	// Hold close() inside cancel so the accepted conn lands in the exact window the
	// ordering governs: whatever close() has already done by the time it calls
	// cancel is what the serve loop observes.
	//
	// Guarded by a Once because close() cancels on every call, including the
	// repeat from the cleanup CloseAll -- harmless for a real context.CancelFunc,
	// which is idempotent, but not for this instrumented stand-in.
	cancelEntered := make(chan struct{})
	releaseCancel := make(chan struct{})
	var cancelOnce sync.Once
	tun.cancel = func() {
		cancelOnce.Do(func() {
			close(cancelEntered)
			<-releaseCancel
			cancelServe()
		})
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	go m.serveSocks5(ctx, tun, nil)

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		tun.close()
	}()
	<-cancelEntered
	close(gate) // deliver the accepted conn into the window

	// failTunnel evicts the tunnel from the manager, so a tunnel still registered
	// is proof no spurious failure was reported.
	require.Never(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, ok := m.tunnels[tun.info.ID]
		return !ok
	}, 300*time.Millisecond, 5*time.Millisecond,
		"an ordinary tunnel close was reported as a listener failure")

	close(releaseCancel)
	<-closed
}

// bindAddrConformanceFixture is the shared cross-language fixture described in
// testdata/tunnel_bind_addr_conformance.json -- read that file's _readme first.
//
// The frontend hand-mirrors net.ParseIP + IsLoopback (frontend/src/lib/ipAddress.ts)
// so the Add-Tunnel dialog can say WHY an address is refused. This side of the
// fixture pins the mirror's ground truth: change what the sidecar accepts without
// changing ipAddress.ts, and the TS suite reading the same file goes red (and vice
// versa). Fields mirror the JSON exactly; `Why` is documentation, not asserted on.
type bindAddrConformanceFixture struct {
	Cases []struct {
		Input      string `json:"input"`
		Normalized string `json:"normalized"`
		Loopback   bool   `json:"loopback"`
		Why        string `json:"why"`
	} `json:"cases"`
}

// loadBindAddrConformance reads the fixture shared with the frontend suite.
//
// go test runs with CWD = the package dir (desktop/go), so the repo-level testdata/
// dir -- the one home reachable from both desktop/go and frontend/src/lib -- is two
// levels up. A file the go tool ignores by name ("testdata") but both languages can
// read is exactly the point.
func loadBindAddrConformance(t *testing.T) bindAddrConformanceFixture {
	t.Helper()
	const path = "../../testdata/tunnel_bind_addr_conformance.json"
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the conformance fixture is shared with frontend/src/lib/ipAddress.test.ts")

	var fixture bindAddrConformanceFixture
	require.NoError(t, json.Unmarshal(raw, &fixture))
	// A fixture that silently loads zero cases would make this test pass while
	// asserting nothing -- the one failure mode a shared fixture must not have.
	require.NotEmpty(t, fixture.Cases, "fixture %s loaded no cases", path)
	return fixture
}

// A tunnel must not be bindable beyond loopback.
//
// Both tunnel types forward into the worker's private network and NEITHER listener
// authenticates (the SOCKS5 server is constructed with no auth methods). BindAddr
// is frontend-supplied and was only defaulted, never validated, so "0.0.0.0" --
// from hostile webview content, or a user who just typed it -- turned the machine
// into an open, unauthenticated SOCKS5 gateway any LAN host could pivot through.
//
// The cases come from the fixture shared with frontend/src/lib/ipAddress.test.ts.
// This side asserts the sidecar half of the contract: the sidecar accepts the value
// the dialog would SUBMIT (`normalized`) iff the dialog predicted acceptance
// (`loopback`). The dialog's own half -- that it normalizes and predicts that way --
// is asserted by the TS suite against the same file.
func TestValidateTunnelBindAddr(t *testing.T) {
	fixture := loadBindAddrConformance(t)

	seen := make(map[string]bool, len(fixture.Cases))
	for _, c := range fixture.Cases {
		require.False(t, seen[c.Input], "duplicate fixture input %q", c.Input)
		seen[c.Input] = true

		t.Run(fmt.Sprintf("%s=>%q", accepts(c.Loopback), c.Normalized), func(t *testing.T) {
			err := validateTunnelBindAddr(c.Normalized)
			if c.Loopback {
				assert.NoError(t, err,
					"fixture says the dialog submits %q for input %q and expects it accepted (%s)",
					c.Normalized, c.Input, c.Why)
				return
			}
			assert.Error(t, err,
				"fixture says %q (from input %q) must be refused (%s); an unauthenticated listener must not be reachable off-machine",
				c.Normalized, c.Input, c.Why)
		})
	}
}

// accepts names a fixture verdict for a subtest name.
func accepts(loopback bool) string {
	if loopback {
		return "allows"
	}
	return "refuses"
}

// The error must say WHICH rule refused, since the dialog mirrors both separately.
func TestValidateTunnelBindAddr_ErrorMessages(t *testing.T) {
	assert.ErrorContains(t, validateTunnelBindAddr("localhost"), "must be an IP address",
		"a hostname is not parseable, and the message should say so rather than blame loopback")
	assert.ErrorContains(t, validateTunnelBindAddr("0.0.0.0"), "loopback",
		"a valid non-loopback IP must be refused for being off-machine")
}

// The validation must be enforced by CreateTunnel, not merely available.
func TestCreateTunnel_RejectsNonLoopbackBindAddr(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)
	ctx := context.Background()

	_, err := m.CreateTunnel(ctx, ctx, TunnelConfig{
		Type:     tunnelTypeSocks5,
		BindAddr: "0.0.0.0",
		WorkerID: "w1",
	})
	require.Error(t, err, "CreateTunnel must refuse an off-machine bind address")
	assert.Contains(t, err.Error(), "loopback")
}

// backoffAfterTemporaryAccept is the wait+backoff step shared by the two accept
// loops (acceptWithRetry and acceptBeforeIdleDeadline). It advances the delay only
// when the wait completes, and returns cancelled the moment its cancel channel fires
// -- the property that lets a teardown abandon a retry storm promptly rather than
// sleeping out a full backoff first.
func TestBackoffAfterTemporaryAccept(t *testing.T) {
	t.Run("completing the wait advances the backoff and does not cancel", func(t *testing.T) {
		cancel := make(chan struct{}) // never fires
		next, cancelled := backoffAfterTemporaryAccept(acceptRetryDelayMin, cancel)
		assert.False(t, cancelled, "an uncancelled wait must not report cancellation")
		assert.Equal(t, nextAcceptRetryDelay(acceptRetryDelayMin), next,
			"a completed wait must advance to the next backoff step")
	})

	t.Run("caps the advanced delay at the ceiling", func(t *testing.T) {
		cancel := make(chan struct{})
		next, cancelled := backoffAfterTemporaryAccept(acceptRetryDelayMax, cancel)
		assert.False(t, cancelled)
		assert.Equal(t, acceptRetryDelayMax, next, "the backoff must not exceed its ceiling")
	})

	t.Run("a fired cancel abandons the wait promptly without advancing the delay", func(t *testing.T) {
		cancel := make(chan struct{})
		close(cancel) // already cancelled

		done := make(chan struct{})
		var next time.Duration
		var cancelled bool
		go func() {
			// A large delay proves the early return is driven by cancel, not the timer.
			next, cancelled = backoffAfterTemporaryAccept(time.Hour, cancel)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a fired cancel must abandon the backoff wait promptly")
		}
		assert.True(t, cancelled, "a fired cancel must report cancellation")
		assert.Equal(t, time.Hour, next, "a cancelled wait must not advance the backoff")
	})
}

// A create and a reconnect both succeed when the Hub announces the expected
// Worker keys: a matching announcement is accepted and never stops a tunnel.
// This is the happy-path control for the mismatch scenarios below. It drives
// the channel layer (the open and the live re-resolve), which is where the key
// is checked, so no listener bind is involved.
func TestTunnelManager_CreateAndReconnectSucceedOnExpectedKey(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	openCalls := 0
	var opened []*managedChannel
	m.pool.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		openCalls++
		ch, _ := newTestManagedChannel()
		opened = append(opened, ch)
		return ch, nil
	}

	expect := channelExpectation{userID: testTunnelOwner, keysHex: testExpectedWorkerKeysHex}
	ctx := context.Background()

	created, err := m.pool.getOrOpen(ctx, m.pool.currentEpoch(), "http://hub", "worker", expect)
	require.NoError(t, err, "creation must succeed on the expected key")
	require.Same(t, opened[0], created)

	// The captured channel dies; a reconnect re-opens on the same expected key
	// and succeeds instead of surfacing a mismatch.
	opened[0].close()
	require.Eventually(t, func() bool {
		m.pool.mu.Lock()
		defer m.pool.mu.Unlock()
		return len(m.pool.channels) == 0
	}, time.Second, time.Millisecond, "the dead channel must leave the cache")

	reconnected, err := m.pool.live(ctx, opened[0], m.pool.currentEpoch(), "http://hub", "worker", expect)
	require.NoError(t, err, "reconnect must succeed on the expected key")
	require.Same(t, opened[1], reconnected)
	assert.False(t, reconnected.closed())
	assert.Equal(t, 2, openCalls, "the reconnect re-opens exactly one channel")
}

// A Worker key mismatch on a reconnect stops a port-forward tunnel BEFORE the
// target dial: the channel is resolved first, so a mismatch can never be
// discovered mid-connection.
func TestTunnelManager_MismatchStopsPortForwardBeforeTargetDial(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	m.pool.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		return nil, &tunnelpkg.WorkerKeyMismatchError{
			ExpectedFingerprint:  "expected-fp",
			PresentedFingerprint: "presented-fp",
		}
	}
	dialed := false
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial after a key mismatch")
	}

	// A closed captured channel forces a re-resolve, which hits the mismatch.
	closedCh, _ := newTestManagedChannel()
	closedCh.close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tun := &tunnel{
		info: TunnelInfo{
			ID: "t-mismatch-pf", WorkerID: "w", Type: tunnelTypePortForward,
			TargetAddr: "target", TargetPort: 80,
		},
		hubURL:        "http://hub",
		listener:      ln,
		cancel:        cancel,
		connections:   make(map[net.Conn]struct{}),
		expect:        channelExpectation{userID: testTunnelOwner, keysHex: testExpectedWorkerKeysHex},
		admittedEpoch: m.pool.currentEpoch(),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	local, err := ln.Accept()
	require.NoError(t, err)

	done := make(chan struct{})
	go func() { defer close(done); m.handlePortForward(ctx, local, tun, closedCh) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handlePortForward did not finish after the key mismatch")
	}

	assert.False(t, dialed, "a key mismatch must stop the tunnel before the target dial")
	rows := listTunnelRows(t, m, testTunnelOwner)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].KeyMismatch, "the mismatch must be recorded on the stopped row")
	assert.Equal(t, "expected-fp", rows[0].KeyMismatch.ExpectedFingerprint)
	assert.Equal(t, "presented-fp", rows[0].KeyMismatch.PresentedFingerprint)
}

// A Worker key mismatch on a reconnect stops a SOCKS5 tunnel BEFORE the target
// dial too: the dial handler resolves the channel first, so the mismatch stops
// the tunnel instead of being found mid-connection.
func TestTunnelManager_MismatchStopsSocks5BeforeTargetDial(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	m.pool.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		return nil, &tunnelpkg.WorkerKeyMismatchError{
			ExpectedFingerprint:  "expected-fp",
			PresentedFingerprint: "presented-fp",
		}
	}
	dialed := false
	m.dial = func(_ context.Context, _ *managedChannel, _ string, _ uint32) (net.Conn, error) {
		dialed = true
		return nil, errors.New("must not dial after a key mismatch")
	}

	closedCh, _ := newTestManagedChannel()
	closedCh.close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tun := &tunnel{
		info:          TunnelInfo{ID: "t-mismatch-socks", WorkerID: "w", Type: tunnelTypeSocks5},
		hubURL:        "http://hub",
		listener:      ln,
		cancel:        cancel,
		connections:   make(map[net.Conn]struct{}),
		expect:        channelExpectation{userID: testTunnelOwner, keysHex: testExpectedWorkerKeysHex},
		admittedEpoch: m.pool.currentEpoch(),
	}
	m.mu.Lock()
	m.tunnels[tun.info.ID] = tun
	m.mu.Unlock()

	go m.serveSocks5(ctx, tun, closedCh)

	// Drive a SOCKS5 CONNECT so the dial handler runs, resolves the channel,
	// hits the mismatch, and stops the tunnel before the target dial.
	client, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.SetDeadline(time.Now().Add(2*time.Second)))
	_, err = client.Write([]byte{5, 1, 0}) // greeting: no-auth
	require.NoError(t, err)
	greeting := make([]byte, 2)
	_, err = io.ReadFull(client, greeting)
	require.NoError(t, err)
	_, err = client.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 80}) // CONNECT 127.0.0.1:80
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		rows := listTunnelRows(t, m, testTunnelOwner)
		return len(rows) == 1 && rows[0].KeyMismatch != nil
	}, 2*time.Second, 10*time.Millisecond, "the key mismatch must stop the SOCKS5 tunnel")

	assert.False(t, dialed, "a key mismatch must stop the SOCKS5 tunnel before the target dial")
	rows := listTunnelRows(t, m, testTunnelOwner)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].KeyMismatch)
	assert.Equal(t, "expected-fp", rows[0].KeyMismatch.ExpectedFingerprint)
	assert.Equal(t, "presented-fp", rows[0].KeyMismatch.PresentedFingerprint)
}

// A Worker key mismatch transition happens exactly once: a second, stale
// announcement must not re-claim the stopped record or overwrite its detail,
// and the change hint fires only once. A late accept failure after the
// transition must not evict the recorded mismatch either -- the row is what the
// UI recovers from.
func TestTunnelManager_StopWithKeyMismatch_IsExactOnce(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	changed := 0
	m.onChanged = func() { changed++ }

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	tun, _ := installTestTunnel(t, m, "t-exact-once", ln)

	first := &TunnelKeyMismatch{ExpectedFingerprint: "exp-1", PresentedFingerprint: "pres-1"}
	second := &TunnelKeyMismatch{ExpectedFingerprint: "exp-2", PresentedFingerprint: "pres-2"}

	m.stopWithKeyMismatch(tun, first)
	// A stale, second announcement must not re-claim the stopped record.
	m.stopWithKeyMismatch(tun, second)

	rows := listTunnelRows(t, m, testTunnelOwner)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].KeyMismatch)
	assert.Equal(t, "exp-1", rows[0].KeyMismatch.ExpectedFingerprint,
		"the first mismatch detail must be preserved, not overwritten by a stale one")
	assert.Equal(t, "pres-1", rows[0].KeyMismatch.PresentedFingerprint)
	assert.Equal(t, 1, changed, "the transition must fire the change hint exactly once")
	assert.True(t, tun.stopping)

	// A late accept failure after the transition must not evict the record.
	m.failTunnel(tun, errors.New("late accept failure"))
	rows = listTunnelRows(t, m, testTunnelOwner)
	require.Len(t, rows, 1, "a late accept failure must not evict the mismatch record")
	assert.NotNil(t, rows[0].KeyMismatch)
}

// A mismatch announcement that arrives after the record is gone (deleted or
// reset) is a no-op: it must not resurrect a row or fire the change hint.
func TestTunnelManager_StopWithKeyMismatch_AfterDeleteIsNoOp(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	changed := 0
	m.onChanged = func() { changed++ }

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	tun, _ := installTestTunnel(t, m, "t-stale", ln)
	require.NoError(t, m.DeleteTunnel("t-stale", m.currentEpoch()))

	m.stopWithKeyMismatch(tun, &TunnelKeyMismatch{ExpectedFingerprint: "exp", PresentedFingerprint: "pres"})

	assert.Empty(t, listTunnelRows(t, m, testTunnelOwner),
		"a stale mismatch must not resurrect a deleted row")
	assert.Zero(t, changed, "a stale mismatch must not fire the change hint")
}

// A create held before native admission, then raced by an identity reset, must
// be fenced: released after the reset it opens no cached channel and registers
// no listener. The reset is an identity change (a transport-option rotation),
// not a teardown, so this exercises the epoch fence on its own.
func TestTunnelManager_HeldCreateBeforeAdmissionIsFencedByReset(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	started := make(chan struct{})
	release := make(chan struct{})
	stale, staleClosed := newTestManagedChannel()
	m.pool.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		close(started)
		<-release
		return stale, nil
	}

	result := make(chan error, 1)
	cfg := withTestExpectation(TunnelConfig{
		HubURL: "http://hub", WorkerID: "worker",
		Type: tunnelTypePortForward, TargetAddr: "target", TargetPort: 80,
		BindAddr: "127.0.0.1", BindPort: 18082,
	})
	cfg.ChannelEpoch = m.currentEpoch()
	go func() {
		_, err := m.CreateTunnel(context.Background(), context.Background(), cfg)
		result <- err
	}()
	<-started // held at the channel open, before native admission

	// An identity change rotates the epoch while the create is held.
	m.SetChannelOptions(&tunnelpkg.OpenChannelOptions{BearerToken: "rotated"})
	close(release)

	err := <-result
	require.Error(t, err, "a create held before admission must be fenced by the reset")
	assert.Contains(t, err.Error(), "reset", "the fence must report the reset, not a random failure")
	assert.True(t, staleClosed.Load(), "the stale channel must be closed, not left open")
	m.mu.Lock()
	assert.Empty(t, m.tunnels, "the fenced create must register no tunnel (no listener admitted)")
	m.mu.Unlock()
	m.pool.mu.Lock()
	assert.Empty(t, m.pool.channels, "the fenced create must leave no cached channel")
	m.pool.mu.Unlock()
}

// A create that carries a channel epoch a reset has already retired is refused
// BEFORE any channel or listener opens: the caller's view is stale, so the
// create must open nothing and force a re-read. This is the "hold an old create,
// reset, release it" case at its sharpest -- the released request opens no
// channel and binds no listener.
func TestTunnelManager_StaleCreateOpensNoChannelOrListener(t *testing.T) {
	m := NewTunnelManager()
	t.Cleanup(m.CloseAll)

	m.pool.openCh = func(_ context.Context, _, _ string, _ *tunnelpkg.OpenChannelOptions) (*managedChannel, error) {
		t.Error("a stale create must not open a channel")
		return nil, errors.New("unreachable")
	}

	stale := m.currentEpoch()
	// The reset retires `stale`; the create below still names it.
	m.ResetTunnels(stale)

	cfg := withTestExpectation(TunnelConfig{
		HubURL: "http://hub", WorkerID: "worker",
		Type: tunnelTypePortForward, TargetAddr: "target", TargetPort: 80,
		BindAddr: "127.0.0.1", BindPort: 18083,
	})
	cfg.ChannelEpoch = stale
	_, err := m.CreateTunnel(context.Background(), context.Background(), cfg)
	require.Error(t, err, "a create carrying a retired epoch must be refused")
	assert.Contains(t, err.Error(), "stale channel epoch", "the refusal must name the stale epoch")
	m.mu.Lock()
	assert.Empty(t, m.tunnels, "a stale create must register no tunnel (no listener admitted)")
	m.mu.Unlock()
}

#!/usr/bin/env perl
# Manual end-to-end regtest harness for dogecade, in the spirit of Dogecoin
# Core's Python functional-test framework: spin up a real `dogecoind
# -regtest`, a real `dogecade` server binary, a mock OIDC provider (so the
# real login flow runs, not a bypass), and a fake Tasmota relay board, then
# drive the whole payment -> credit -> redeem -> relay-fire pipeline over
# HTTP exactly as a browser would.
#
# Usage: perl scripts/regtest/e2e.pl
#
# Requires: dogecoind on PATH (or DOGECOIND_BIN env var), Go toolchain,
# Perl with Mojolicious/LWP::UserAgent/Crypt::PK::RSA (all already present
# on this machine).
use strict;
use warnings;
use FindBin qw($Bin);
use File::Temp qw(tempdir);
use File::Spec;
use LWP::UserAgent;
use HTTP::Cookies;
use HTTP::Request::Common qw(GET POST);
use URI;
use JSON::PP qw(encode_json decode_json);
use Time::HiRes qw(sleep time);

my $REPO_ROOT = File::Spec->rel2abs("$Bin/../..");
my @CHILDREN; # pids to kill on exit

$SIG{INT} = $SIG{TERM} = sub { cleanup(); exit 1 };
END { cleanup() }

sub cleanup {
    for my $pid (@CHILDREN) {
        next unless $pid;
        kill 'TERM', $pid;
    }
    for my $pid (@CHILDREN) {
        next unless $pid;
        waitpid($pid, 0);
    }
    @CHILDREN = ();
}

sub fail { print STDERR "FAIL: $_[0]\n"; cleanup(); exit 1; }
sub step { print "\n=== $_[0] ===\n"; }
sub ok   { print "  ok: $_[0]\n"; }

# ---------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------

sub free_port {
    require IO::Socket::INET;
    my $sock = IO::Socket::INET->new(Listen => 1, LocalAddr => '127.0.0.1', ReuseAddr => 1)
        or die "can't find a free port: $!";
    my $port = $sock->sockport;
    $sock->close;
    return $port;
}


# Forks+execs a long-running server, redirecting its stdout/stderr to
# $logfile. Using a plain pipe (open '-|') here would deadlock: these are
# Mojolicious daemons that keep logging every request to stdout forever, and
# we only read a line or two before moving on, so the pipe buffer fills and
# the child blocks mid-request forever. A file sidesteps that entirely.
sub spawn_logged {
    my ($desc, $logfile, @cmd) = @_;
    my $pid = fork();
    fail("fork failed for $desc") unless defined $pid;
    if ($pid == 0) {
        open(STDOUT, '>', $logfile);
        open(STDERR, '>&STDOUT');
        exec(@cmd);
        exit 1;
    }
    push @CHILDREN, $pid;
    ok("$desc started (pid $pid)");
    return $pid;
}

sub http_wait {
    my ($url, $timeout) = @_;
    my $ua = LWP::UserAgent->new(timeout => 2);
    my $deadline = time() + $timeout;
    while (time() < $deadline) {
        my $resp = $ua->get($url);
        return 1 if $resp->is_success || $resp->code == 401 || $resp->code == 403;
        sleep 0.2;
    }
    return 0;
}

# JSON-RPC call against dogecoind.
my ($DOGE_RPC_URL, $DOGE_RPC_USER, $DOGE_RPC_PASS);
sub doge_rpc {
    my ($method, @params) = @_;
    my $ua = LWP::UserAgent->new;
    my $req = POST $DOGE_RPC_URL,
        Content_Type => 'application/json',
        Content      => encode_json({ jsonrpc => '1.0', id => 'e2e', method => $method, params => \@params });
    $req->authorization_basic($DOGE_RPC_USER, $DOGE_RPC_PASS);
    my $resp = $ua->request($req);
    die("dogecoind RPC $method failed: " . $resp->status_line . " " . $resp->decoded_content . "\n")
        unless $resp->is_success;
    my $data = decode_json($resp->decoded_content);
    die("dogecoind RPC $method error: " . encode_json($data->{error}) . "\n") if $data->{error};
    return $data->{result};
}

# Drives the real OIDC authorization-code flow against our mock provider
# through dogecade's actual /auth/login and /auth/callback handlers, using
# ua's cookie jar for session state. Returns once the session cookie is set.
sub oidc_login {
    my ($ua, $base_url, $oidc_url, $subject, $name, $email) = @_;
    $ua->requests_redirectable([]); # follow redirects manually so we can hop origins

    my $resp = $ua->get("$base_url/auth/login?redirect=/");
    fail("login start failed: " . $resp->status_line) unless $resp->code == 302;
    my $authorize_url = $resp->header('Location');

    my $u = URI->new($authorize_url);
    $u->query_form($u->query_form, login_hint => $subject, name => $name, email => $email);
    $resp = $ua->get($u);
    fail("authorize step failed: " . $resp->status_line) unless $resp->code == 302;
    my $callback_url = $resp->header('Location');

    $resp = $ua->get($callback_url);
    fail("callback step failed: " . $resp->status_line . " " . $resp->decoded_content) unless $resp->code == 302;
    ok("signed in as $subject via mock OIDC");
}

sub form_post {
    my ($ua, $url, %fields) = @_;
    my $resp = $ua->post($url, \%fields);
    return $resp;
}

# ---------------------------------------------------------------------
# 0. workspace
# ---------------------------------------------------------------------
my $tmp = tempdir("dogecade-e2e-XXXXXX", TMPDIR => 1, CLEANUP => !$ENV{E2E_KEEP_WORKSPACE});
print "workspace: $tmp\n";

step "Locate dogecoind";
my $dogecoind_bin = $ENV{DOGECOIND_BIN};
unless ($dogecoind_bin) {
    for (split /:/, $ENV{PATH}) {
        my $c = "$_/dogecoind";
        if (-x $c) { $dogecoind_bin = $c; last; }
    }
}
fail("dogecoind not found; set DOGECOIND_BIN=/path/to/dogecoind") unless $dogecoind_bin && -x $dogecoind_bin;
ok("using $dogecoind_bin");

step "Build dogecade";
my $dogecade_bin = "$tmp/dogecade";
system("cd '$REPO_ROOT' && go build -o '$dogecade_bin' ./cmd/dogecade") == 0
    or fail("go build failed");
ok("built $dogecade_bin");

# ---------------------------------------------------------------------
# 1. dogecoind -regtest
# ---------------------------------------------------------------------
step "Start dogecoind -regtest";
my $doge_datadir = "$tmp/dogecoin-regtest";
mkdir $doge_datadir;
my $doge_rpc_port = free_port();
$DOGE_RPC_URL  = "http://127.0.0.1:$doge_rpc_port";
$DOGE_RPC_USER = "e2euser";
$DOGE_RPC_PASS = "e2epass";

my $doge_pid = fork();
fail("fork failed") unless defined $doge_pid;
if ($doge_pid == 0) {
    open(STDOUT, '>', "$tmp/dogecoind.log");
    open(STDERR, '>&STDOUT');
    exec($dogecoind_bin,
        '-regtest', "-datadir=$doge_datadir",
        "-rpcuser=$DOGE_RPC_USER", "-rpcpassword=$DOGE_RPC_PASS",
        "-rpcport=$doge_rpc_port", '-daemon=0', '-fallbackfee=0.001',
        '-listen=0');
    exit 1;
}
push @CHILDREN, $doge_pid;

{
    my $deadline = time() + 20;
    my $up = 0;
    while (time() < $deadline) {
        eval { doge_rpc('getblockchaininfo'); $up = 1; };
        last if $up;
        sleep 0.3;
    }
    fail("dogecoind RPC never came up (see $tmp/dogecoind.log)") unless $up;
}
ok("dogecoind RPC responsive on port $doge_rpc_port");

eval { doge_rpc('createwallet', 'e2e') };
my $mine_addr = doge_rpc('getnewaddress');
doge_rpc('generatetoaddress', 101, $mine_addr);
ok("mined 101 blocks to $mine_addr");

# ---------------------------------------------------------------------
# 2. mock OIDC provider + fake Tasmota relay
# ---------------------------------------------------------------------
step "Start mock OIDC provider";
my $oidc_port = free_port();
spawn_logged("mock OIDC provider", "$tmp/mock_oidc.log", 'perl', "$Bin/mock_oidc.pl", $oidc_port, "$tmp/oidc.key");
my $oidc_url = "http://127.0.0.1:$oidc_port";
http_wait("$oidc_url/.well-known/openid-configuration", 15) or fail("mock OIDC never became reachable (see $tmp/mock_oidc.log)");
ok("mock OIDC discovery reachable");

step "Start fake Tasmota relay";
my $relay_port = free_port();
my $relay_log  = "$tmp/relay.log";
open(my $rl, '>', $relay_log) or fail("create relay log: $!"); close $rl;
spawn_logged("fake Tasmota relay", "$tmp/fake_relay.log", 'perl', "$Bin/fake_relay.pl", $relay_port, $relay_log);
my $relay_url = "http://127.0.0.1:$relay_port";
http_wait("$relay_url/cm?cmnd=Status", 15) or fail("fake relay never became reachable (see $tmp/fake_relay.log)");
ok("fake Tasmota relay reachable");

# ---------------------------------------------------------------------
# 3. dogecade server
# ---------------------------------------------------------------------
step "Start dogecade server";
my $dogecade_port = free_port();
my $dogecade_base = "http://127.0.0.1:$dogecade_port";
my $admin_subject = "admin1";

my $dogecade_pid = fork();
fail("fork failed") unless defined $dogecade_pid;
if ($dogecade_pid == 0) {
    open(STDOUT, '>', "$tmp/dogecade.log");
    open(STDERR, '>&STDOUT');
    %ENV = (%ENV,
        DOGECADE_DB_PATH         => "$tmp/dogecade.db",
        DOGECADE_BASE_URL        => $dogecade_base,
        DOGECADE_LISTEN_ADDR     => ":$dogecade_port",
        DOGECOIND_RPC_URL        => $DOGE_RPC_URL,
        DOGECOIND_RPC_USER       => $DOGE_RPC_USER,
        DOGECOIND_RPC_PASS       => $DOGE_RPC_PASS,
        DOGECADE_ADMIN_SUBJECTS  => "$oidc_url|$admin_subject",
        DOGECADE_OIDC_ISSUER_URL => $oidc_url,
        DOGECADE_OIDC_CLIENT_ID  => "dogecade-e2e",
        DOGECADE_OIDC_CLIENT_SECRET => "not-checked-by-mock",
        DOGECADE_SESSION_SECRET  => "e2e-session-secret-not-for-prod-0000000000000",
    );
    exec($dogecade_bin, 'serve');
    exit 1;
}
push @CHILDREN, $dogecade_pid;
http_wait("$dogecade_base/healthz", 15) or fail("dogecade server never became healthy (see $tmp/dogecade.log)");
ok("dogecade listening on $dogecade_base");

# ---------------------------------------------------------------------
# 4. admin: import addresses, create machine, wire relay
# ---------------------------------------------------------------------
step "Admin: sign in";
my $admin_ua = LWP::UserAgent->new(cookie_jar => HTTP::Cookies->new);
oidc_login($admin_ua, $dogecade_base, $oidc_url, $admin_subject, 'Admin', 'admin@example.test');

step "Admin: import a token_deposit address";
my $deposit_addr = doge_rpc('getnewaddress');
my $resp = form_post($admin_ua, "$dogecade_base/admin/addresses/import",
    addresses => $deposit_addr, purpose => 'token_deposit');
fail("address import failed: " . $resp->status_line) unless $resp->is_success || $resp->code == 302 || $resp->code == 303;
if ($resp->code == 200 && $resp->decoded_content =~ /class="error"[^>]*>([^<]+)</) {
    fail("address import rejected: $1");
}
ok("imported $deposit_addr as token_deposit");

step "Admin: create machine + relay board + binding";
my $slug = "cab1";
$resp = form_post($admin_ua, "$dogecade_base/admin/machines", slug => $slug, name => "Cabinet One");
fail("machine create failed: " . $resp->status_line) unless $resp->is_success || $resp->code == 302 || $resp->code == 303;

$resp = form_post($admin_ua, "$dogecade_base/admin/boards", name => 'board1', base_url => $relay_url);
fail("board create failed: " . $resp->status_line) unless $resp->is_success || $resp->code == 302 || $resp->code == 303;

$resp = $admin_ua->get("$dogecade_base/admin/machines");
fail("failed to reload admin machines page: " . $resp->status_line) unless $resp->is_success;
my $html = $resp->decoded_content;
my ($machine_id) = $html =~ /machines\/(\d+)\/toggle/;
my ($board_id)   = $html =~ /boards\/(\d+)\/toggle/;
fail("could not scrape machine_id/board_id from admin page") unless $machine_id && $board_id;

$resp = form_post($admin_ua, "$dogecade_base/admin/relays/bind",
    machine_id => $machine_id, board_id => $board_id, relay_number => 1);
fail("relay bind failed: " . $resp->status_line) unless $resp->is_success || $resp->code == 302 || $resp->code == 303;
ok("machine '$slug' (id $machine_id) bound to board '$relay_url' relay 1");

# ---------------------------------------------------------------------
# 5. customer: buy tokens with a real regtest payment
# ---------------------------------------------------------------------
step "Customer: sign in";
my $cust_ua = LWP::UserAgent->new(cookie_jar => HTTP::Cookies->new, timeout => 5);
oidc_login($cust_ua, $dogecade_base, $oidc_url, "cust1", 'Customer One', 'cust1@example.test');

step "Customer: start a purchase";
$resp = $cust_ua->get("$dogecade_base/buy");
fail("GET /buy failed: " . $resp->status_line) unless $resp->is_success;
my ($pay_addr) = $resp->decoded_content =~ /id="address">([^<]+)</;
unless ($pay_addr) {
    print STDERR "---- /buy page content ----\n" . $resp->decoded_content . "\n----------------------------\n";
}
fail("could not find deposit address on /buy page") unless $pay_addr;
ok("assigned deposit address: $pay_addr");

step "Send a real regtest payment to that address";
doge_rpc('sendtoaddress', $pay_addr, 1); # 1 DOGE == 1 token at default price
doge_rpc('generatetoaddress', 2, $mine_addr); # min_confirmations default is 1
ok("sent 1 DOGE and mined 2 confirming blocks");

step "Wait for dogecade to detect + credit the deposit";
{
    my $deadline = time() + 30;
    my $state = '';
    while (time() < $deadline) {
        my $r = $cust_ua->get("$dogecade_base/buy/status?address=$pay_addr");
        if ($r->is_success) {
            my $data = eval { decode_json($r->decoded_content) };
            $state = $data->{state} // '' if $data;
            last if $state eq 'credited';
        }
        sleep 1;
    }
    fail("deposit was never credited (last state: '$state'); check $tmp/dogecade.log") unless $state eq 'credited';
}
ok("deposit credited");

$resp = $cust_ua->get("$dogecade_base/history");
fail("GET /history failed: " . $resp->status_line) unless $resp->is_success;
ok("ledger history reachable") if $resp->decoded_content =~ /purchase|credit/i;

# ---------------------------------------------------------------------
# 6. customer: redeem a token at the machine, confirm the relay fired
# ---------------------------------------------------------------------
step "Customer: redeem a token at $slug";
$resp = form_post($cust_ua, "$dogecade_base/m/$slug");
fail("redeem POST failed: " . $resp->status_line) unless $resp->is_success;
fail("redeem page did not report success") unless $resp->decoded_content =~ /redeem|credit|success/i;
ok("redemption request accepted");

step "Wait for the relay pulse to reach the fake Tasmota board";
{
    my $deadline = time() + 20;
    my $fired = 0;
    while (time() < $deadline) {
        if (-s $relay_log) {
            open(my $fh, '<', $relay_log);
            while (<$fh>) {
                if (/Power1\s+ON/i) { $fired = 1; last; }
            }
            close $fh;
        }
        last if $fired;
        sleep 1;
    }
    fail("relay never fired; check $relay_log and $tmp/dogecade.log") unless $fired;
}
ok("relay board received Power1 ON pulse");

print "\nALL CHECKS PASSED\n";
print "workspace preserved at: $tmp (dogecade.log, dogecoind.log, relay.log)\n" if $ENV{E2E_KEEP_WORKSPACE};
cleanup();
exit 0;

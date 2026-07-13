#!/usr/bin/env perl
# Minimal OIDC provider for driving dogecade's real OIDC login flow against
# a local regtest harness, without a live Google/Okta/whatever issuer.
#
# Endpoints:
#   GET  /.well-known/openid-configuration
#   GET  /jwks.json
#   GET  /authorize   -- auto-approves; ?login_hint=<subject>&name=<n>&email=<e>
#                         picks who "logs in" (no real login UI/consent screen)
#   POST /token       -- exchanges the code for a signed RS256 id_token
#
# Usage: mock_oidc.pl <port> <keyfile>
# Writes nothing to stdout except "READY <port>" once listening.
use strict;
use warnings;
use Mojolicious::Lite -signatures;
use Crypt::PK::RSA;
use MIME::Base64 qw(encode_base64url decode_base64url decode_base64);
use JSON::PP qw(encode_json decode_json);
use Digest::SHA qw(sha256);

my $port    = shift @ARGV or die "usage: mock_oidc.pl <port> <keyfile>\n";
my $keyfile = shift @ARGV or die "usage: mock_oidc.pl <port> <keyfile>\n";

my $pk = Crypt::PK::RSA->new;
$pk->generate_key(256, 65537); # 2048-bit
open my $fh, '>', $keyfile or die "write $keyfile: $!";
print $fh $pk->export_key_pem('private');
close $fh;

my $issuer = "http://127.0.0.1:$port";
my %codes; # code => { subject, email, name, nonce, redirect_uri }

sub b64url_json { encode_base64url(encode_json($_[0])) =~ tr/=//dr }

sub jwk_public {
    my $jwk = decode_json($pk->export_key_jwk('public'));
    $jwk->{use} = 'sig';
    $jwk->{alg} = 'RS256';
    $jwk->{kid} = 'mock-key-1';
    return $jwk;
}

sub sign_id_token ($subject, $email, $name, $nonce, $aud) {
    my $header  = { alg => 'RS256', typ => 'JWT', kid => 'mock-key-1' };
    my $now     = time();
    my $payload = {
        iss   => $issuer,
        sub   => $subject,
        aud   => $aud,
        exp   => $now + 300,
        iat   => $now,
        nonce => $nonce,
        email => $email,
        name  => $name,
    };
    my $signing_input = b64url_json($header) . '.' . b64url_json($payload);
    my $sig = $pk->sign_message($signing_input, 'SHA256', 'v1.5');
    return $signing_input . '.' . (encode_base64url($sig) =~ tr/=//dr);
}

get '/.well-known/openid-configuration' => sub ($c) {
    $c->render(json => {
        issuer                 => $issuer,
        authorization_endpoint => "$issuer/authorize",
        token_endpoint         => "$issuer/token",
        jwks_uri               => "$issuer/jwks.json",
        response_types_supported => ['code'],
        subject_types_supported   => ['public'],
        id_token_signing_alg_values_supported => ['RS256'],
    });
};

get '/jwks.json' => sub ($c) {
    $c->render(json => { keys => [ jwk_public() ] });
};

# Auto-approving "login": whoever calls /authorize picks the identity via
# login_hint, since there's no real user sitting at a browser here.
get '/authorize' => sub ($c) {
    my $redirect_uri = $c->param('redirect_uri');
    my $state         = $c->param('state');
    my $nonce          = $c->param('nonce') // '';
    my $subject        = $c->param('login_hint') // 'regtest-user';
    my $email           = $c->param('email') // "$subject\@example.test";
    my $name             = $c->param('name') // $subject;

    my $code = encode_base64url(sha256(rand() . $subject . time())) =~ tr/=//dr;
    $codes{$code} = {
        subject => $subject, email => $email, name => $name, nonce => $nonce,
    };

    my $url = Mojo::URL->new($redirect_uri);
    $url->query->append(code => $code, state => $state);
    $c->redirect_to($url);
};

post '/token' => sub ($c) {
    my $code = $c->param('code');
    # golang.org/x/oauth2 defaults to HTTP Basic auth for client
    # credentials rather than form params, so check both.
    my $client_id = $c->param('client_id');
    if (!$client_id && (my $auth = $c->req->headers->authorization)) {
        if ($auth =~ /^Basic\s+(.+)$/) {
            my $decoded = decode_base64($1);
            ($client_id) = split(/:/, $decoded, 2);
        }
    }
    my $entry = delete $codes{$code};
    return $c->render(json => { error => 'invalid_grant' }, status => 400) unless $entry;

    my $id_token = sign_id_token($entry->{subject}, $entry->{email}, $entry->{name}, $entry->{nonce}, $client_id);
    $c->render(json => {
        access_token => 'mock-access-token',
        token_type   => 'Bearer',
        expires_in   => 300,
        id_token     => $id_token,
    });
};

local $| = 1;
print "READY $port\n";

app->start('daemon', '-l', "http://127.0.0.1:$port");

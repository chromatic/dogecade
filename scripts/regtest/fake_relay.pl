#!/usr/bin/env perl
# Fake Tasmota relay board: implements enough of the "/cm?cmnd=..." console
# interface for internal/relay/client.go's Pulse()/Status() to work against,
# and logs every command it receives (one JSON line per call) to a log file
# so the harness can assert a relay actually fired.
#
# Usage: fake_relay.pl <port> <logfile>
use strict;
use warnings;
use Mojolicious::Lite -signatures;
use JSON::PP qw(encode_json);

my $port    = shift @ARGV or die "usage: fake_relay.pl <port> <logfile>\n";
my $logfile = shift @ARGV or die "usage: fake_relay.pl <port> <logfile>\n";

open my $log, '>>', $logfile or die "write $logfile: $!";
$log->autoflush(1);

get '/cm' => sub ($c) {
    my $cmnd = $c->param('cmnd') // '';
    print $log encode_json({ t => time(), cmnd => $cmnd }), "\n";

    if ($cmnd =~ /^Status$/i) {
        return $c->render(json => { Status => { Module => 1, DeviceName => 'fake-relay' } });
    }
    # Backlog PulseTime<N> <secs>; Power<N> ON  -- Tasmota echoes back a
    # POWERn state for the Power command in the Backlog.
    if ($cmnd =~ /Power(\d+)\s+ON/i) {
        return $c->render(json => { "POWER$1" => "ON" });
    }
    $c->render(json => { Command => 'Unknown' });
};

local $| = 1;
print "READY $port\n";

app->start('daemon', '-l', "http://127.0.0.1:$port");

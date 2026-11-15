use polaris_ratelimit::client_key;
use std::net::IpAddr;

fn ip(s: &str) -> IpAddr {
    s.parse().unwrap()
}

#[test]
fn the_cloudflare_header_wins_because_a_client_cannot_overwrite_it() {
    assert_eq!(
        client_key(ip("127.0.0.1"), Some("203.0.113.9"), Some("198.51.100.7")),
        ip("203.0.113.9")
    );
}

#[test]
fn a_loopback_peer_falls_back_to_the_forwarded_address() {
    assert_eq!(
        client_key(ip("127.0.0.1"), None, Some("203.0.113.9")),
        ip("203.0.113.9")
    );
    assert_eq!(
        client_key(ip("127.0.0.1"), None, Some("203.0.113.9, 10.0.0.1")),
        ip("203.0.113.9")
    );
}

#[test]
fn headers_from_a_non_loopback_peer_are_ignored() {
    assert_eq!(
        client_key(ip("198.51.100.4"), Some("203.0.113.9"), Some("203.0.113.9")),
        ip("198.51.100.4")
    );
}

#[test]
fn a_malformed_header_falls_back_to_the_next_source() {
    assert_eq!(
        client_key(ip("127.0.0.1"), Some("not-an-ip"), Some("203.0.113.9")),
        ip("203.0.113.9")
    );
    assert_eq!(
        client_key(ip("127.0.0.1"), Some("not-an-ip"), Some("also-bad")),
        ip("127.0.0.1")
    );
    assert_eq!(client_key(ip("127.0.0.1"), None, None), ip("127.0.0.1"));
}

//! Who the client is, and the rules that make the answer trustworthy.

use std::net::IpAddr;

/// The address a request is budgeted against.
///
/// nginx fronts every product, so a real client always arrives from loopback and a header is the only thing
/// that distinguishes them. The headers are read **only** when the peer is loopback: a non-loopback caller
/// that sends one is either a proxy nobody configured or someone minting identities.
///
/// `connecting_ip` is `CF-Connecting-IP`, which Cloudflare sets and **overwrites** on every request, so it is
/// the one header a client cannot choose. `forwarded_for` is `X-Forwarded-For`, which nginx *appends* to — its
/// first element is whatever the caller sent — so it is the fallback for a deployment without Cloudflare, never
/// the primary. This order was measured, not assumed: suki's limiter already had it, and a crate that trusted
/// `X-Forwarded-For` first would hand a scraper a new identity per request.
///
/// A malformed header is ignored rather than fatal: a limiter that panics on a header is a denial of service.
pub fn client_key(
    peer: IpAddr,
    connecting_ip: Option<&str>,
    forwarded_for: Option<&str>,
) -> IpAddr {
    if !peer.is_loopback() {
        return peer;
    }
    connecting_ip
        .and_then(parse_ip)
        .or_else(|| {
            forwarded_for
                .and_then(|value| value.split(',').next())
                .and_then(parse_ip)
        })
        .unwrap_or(peer)
}

fn parse_ip(raw: &str) -> Option<IpAddr> {
    raw.trim().parse::<IpAddr>().ok()
}

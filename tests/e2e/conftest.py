"""Shared e2e setup.

One thing lives here: the cookie notice is accepted on behalf of the visitor.

Why it must be, and why it is not a loophole. Interactions are refused, by the
server, until the notice is accepted (internal/httpapi/guest_handlers.go). A real
visitor clicks Accept once and every later request carries the cookie. A suite whose
every mutating step answered 403 would be testing the gate on every test in the
directory, and the gate has its own file.

So the harness does what the visitor did. `tests/e2e/guest_e2e.py` is the file that
tests the notice itself, and it clears the cookie at the start of each test to get
back to the state a first-time visitor is in.
"""

import pytest
from playwright.sync_api import Browser


NOTICE_COOKIE = {
    "name": "concord_consent",
    "value": "1",
    "domain": "127.0.0.1",
    "path": "/",
}


@pytest.fixture(autouse=True)
def _accept_cookie_notice(monkeypatch):
    """Give every browser context an accepted notice, the way a visitor would.

    Patched at the class rather than in each of the ~20 `page` fixtures: the rule is
    about the visitor, not about a page, and a per-fixture opt-in is a fixture
    somebody adds later without it.
    """
    original_context = Browser.new_context

    def new_context(self, *args, **kwargs):
        ctx = original_context(self, *args, **kwargs)
        ctx.add_cookies([NOTICE_COOKIE])
        return ctx

    original_page = Browser.new_page

    def new_page(self, *args, **kwargs):
        pg = original_page(self, *args, **kwargs)
        pg.context.add_cookies([NOTICE_COOKIE])
        return pg

    monkeypatch.setattr(Browser, "new_context", new_context)
    monkeypatch.setattr(Browser, "new_page", new_page)

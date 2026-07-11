# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository purpose

This repository contains a single book chapter, `docs/chapter_dogecade.pod`, written in Perl's POD (Plain Old Documentation) format. There is no application code, build system, test suite, or package manifest in this repo — it is documentation-only. Do not assume or invent build/lint/test commands; none exist.

The chapter ("Manage a Dogecoin Arcade") is a design-and-implementation walkthrough for accepting Dogecoin payments to trigger real-world hardware (arcade cabinets, pinball machines, vending-style devices) and other token/ticketing systems. Code samples embedded in the prose (SQL schemas, Perl snippets using `Finance::Libdogecoin` and `Object::Pad`) are illustrative examples for readers, not part of a working codebase.

## Working with the POD file

- POD syntax: `=head0`/`=head1`/`=head2`/`=head3` are section headers; `Z<label>` defines an anchor; `L<label>` cross-references another anchor elsewhere in the book (these labels resolve outside this repo, in the larger book project — don't treat unresolved `L<...>` targets as broken links to fix here); `N<...>` is a footnote; `X<...>` is an index entry; `=begin screen`/`=end screen` and `=begin programlisting`/`=end programlisting` wrap code/terminal examples; `=begin tip ...`/`=end tip` wraps callout boxes; `C<...>`, `B<...>`, `I<...>` are code/bold/italic inline formatting.
- Preserve this markup precisely when editing prose — POD renders based on exact tag matching.
- Maintain the book's tone: casual, humor-laden, second-person ("you"), full of arcade/pinball and Dogecoin-culture references. Match this voice for any new or edited prose rather than switching to a dry technical register.
- Each major section ends with an "Understand the Risks" subsection discussing security/privacy/operational tradeoffs — follow this pattern if adding a new top-level section.

## Conceptual architecture described in the chapter

The chapter builds up one coherent system across its sections, referenced by anchor name:

1. **`associate_addresses_to_machines`** — map Dogecoin addresses to arcade machines (by DNS hostname) via a SQLite table `addresses_to_machines(address, dns_name, is_active)`, so incoming payments can be routed to the right cabinet.
2. **`practice_safe_wallet_hygiene`** / **`rotate_machine_addresses`** — avoid address reuse for privacy; periodically rotate which address is active per machine, sourcing fresh addresses from a `wallet_addresses(address, label)` table, using a transactional SQL script (CTEs + `ROW_NUMBER()` to pair unused addresses with machines).
3. **`manage_tokens`** — an alternative/complementary design: sell customers a token balance for Dogecoin (via a per-customer receiving address), then redeem tokens for machine credits, decoupling on-chain confirmation latency from gameplay. Requires an identity/accounting layer; recommends hashing (not storing plaintext) customer identifiers.
4. **`sell_event_admission`** — reuses the same "generate a unique address, watch for payment, mark a semi-secret admission code valid" pattern for event ticketing instead of arcade credits.
5. **`derive_more_addresses`** — generating the steady supply of addresses needed above via BIP-32/BIP-44 HD wallet derivation (`m/44'/3'/0'/0/index`), using `libdogecoin` (e.g. via Perl's `Finance::Libdogecoin`), and the security tradeoff of keeping the master key encrypted at rest vs. in-memory exposure.
6. **`program_real_buttons`** / **`flip_a_switch`** — the hardware bridge: an ESP8266-based programmable relay board (Tasmota firmware) wired into a cabinet's coin-switch circuit, controlled over HTTP/MQTT (e.g. `curl http://<board>/cm?cmnd=Power1%20On`), driven by a webhook fired when a tracked address receives payment. A second table, `addresses_to_relays(machine, dns_name, relay, is_active)`, maps machines to specific relay boards/channels.
7. **`install_programmable_buttons`** — a concrete worked example wiring this relay setup into a real Stern Lord of the Rings pinball machine's coin mechanism and power connector.

When editing or extending this chapter, keep the data flow consistent across sections: **address generation (HD derivation) → address/machine (or address/customer) association (SQLite tables) → payment detection (node/webhook, described elsewhere in the book) → relay/webhook trigger → physical switch toggle**.

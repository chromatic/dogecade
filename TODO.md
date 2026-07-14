# Open Tasks

As of this writing in July 2026, this project has a long way to go! Please feel
free to take on any of these tasks as you have interest or ability.

## Security Improvements

I've reviewed the codebase for security and found and fixed a few issues, but
undoubtedly there's always more to review. Any findings are welcome.

## User Experience Improvements

The current user experience is minimal and undoubtedly has some flaws. While
the very basic user flow is probably sufficient, the administrative interface
needs some attention.

## Visual Improvements

The design is minimal and does not easily allow rebranding. Make it possible
and then easy to add custom display, icons, and visual elements for the web UI,
including custom arcade branding and machine display.

## Cryptocurrency Expansion

The current implementation supports Dogecoin. Any Scrypt coin of similar
heritage could work. In particular, Pepecoin and Litecoin should be easy to add
and test.

While adopting `libdogecoin`'s SPV approach would simplify the implementation,
it could lock in the codebase to CGO and make it more difficult to support
other coins. This may still be the right technical approach, though I prefer
SPV without Dogecoin lockin.

## Hardware Expansion

Right now the machine notification system uses the Tasmota-style programming
found in Dogecoin Tricks. This approach works well enough for what it is, but
it could expand to support other mechanisms.

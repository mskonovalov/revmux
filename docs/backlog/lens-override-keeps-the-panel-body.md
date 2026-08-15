---
worth: later
where: app/prompt/roster.go:156
added: 2026-08-15
---
# `--lenses` against a panel profile composes a body claiming absent panelists

`Roster` replaces the whole roster with one agent when `lensOverride` is non-empty, but `Compose`
(`app/prompt/compose.go:38`) still prepends the profile body unchanged. Against `triage` that body opens
"You are one panelist on a four-way panel... the other three panelists are working the same item with the
other parts. You never see their output and must not guess at it", so `--profile triage --lenses cost`
runs a single agent told three peers exist. Nothing warns.

The same shape reaches any profile whose body describes its roster. `triage` is the only shipped one that
does today, which is why this has not bitten.

Three defensible answers and no obvious winner, which is why this is `later` rather than `yes`: warn when a
profile body and the resolved roster disagree, refuse the combination for profiles that declare a panel, or
document `--lenses` as unsupported against `triage` and leave the code alone. The first needs a way to know
a body describes its roster, which nothing currently carries.

Surfaced answering discussion #8, from a cheaper-single-agent idea that would have hit exactly this.

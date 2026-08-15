---
worth: yes
where: site/reference.html:437
added: 2026-08-15
---
# the reference tables gloss `immaterial` only in its code-review sense

`site/reference.html:437` and `:457` both read `immaterial` as "true and not worth acting on", which is
the shipped `verify.md:35` wording for a review of a change. For a filed item that is not what the verdict
means: `verify.md:41-65` has a "When a finding names no file" section that turns `pre_existing` off
entirely and rereads `immaterial` as "the point does not bear on the decision being asked for, not that a
defect is not worth fixing". So a `triage` run's verdicts are documented under a definition the binary does
not use for them.

`site/docs.html:300` and `:811` enumerate the verdicts without glossing either one, so they are not wrong,
just thin. The reference is the only place a caller looks up what a verdict means, and it is the one that
carries the wrong reading.

Surfaced answering discussion #8, where the reporter concluded the verify verdicts could not adjudicate his
question. They can, and the reference is why he thought otherwise. Deferred rather than fixed inline because
the second reading has to fit a one-line table cell without making the code-review case ambiguous, and that
is a wording call.

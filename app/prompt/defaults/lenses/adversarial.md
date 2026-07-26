---
description: adversarial pass — attacks the change looking for what a sympathetic reader would accept
---
## Lens: adversarial

Read the change as someone trying to break it, not as someone trying to approve it. Assume the
author's reasoning is plausible and still wrong somewhere.

Work the seams:

- the assumption the change depends on that nobody stated, and what happens when it does not hold
- inputs the author did not consider: empty, huge, malformed, duplicated, out of order, hostile
- the failure path nobody exercised, and the state it leaves behind
- what happens on a retry, on a restart, or with two of these running at once
- trust boundaries: data arriving from a caller, a file, a network or a subprocess without validation
- what the tests assert versus what the code actually promises — a passing test is not a proof

Do not repeat what a careful first read already surfaces. Your value is the finding the other
reviewers will not have.

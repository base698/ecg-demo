# Semantic answers and runtime claims

A successful voice or HTTP interaction can contain an unsupported answer. ECG
already separates execution coverage from application-owned correctness checks.
It does not automatically understand speech or determine whether a claim is true.

For a question such as which API an assistant uses, the application needs two
inputs: an explicit claim and a trusted runtime fact. A generated transcript is
an observation of an interaction, not an authoritative source of runtime identity.
A transcription disclaimer does not make the assistant's answer accurate.

| Situation | Application check | ECG result at the coverage minimum |
|---|---|---|
| Explicit claim agrees with a verified runtime fact | passed | satisfied in coverage mode |
| Claim contradicts the verified fact | failed | failed, even when the request succeeded |
| Answer is vague or contains no verifiable claim | unknown | inconclusive |
| Runtime identity is unavailable or unverified | unknown | inconclusive |
| Required scenario has never run | no observation | missing |

`examples/claims` implements this narrow comparison with invented fixture
identifiers and drives it through the real SDK and evaluator. It demonstrates
the existing evidence contract; it is not a production voice integration.
The six fixture repetitions are enough for demo coverage, not statistical
confidence or proof of general factual accuracy.

## Application responsibilities

- Obtain runtime facts from trusted configuration, an allowlisted tool result,
  or a verified deployment manifest. Do not ask a model to invent its own
  provider, API, model, or permissions and then treat that answer as evidence.
- Enforce disclosure policy separately. A fact being true does not authorize
  revealing it. If the assistant cannot verify or disclose an answer, it should
  say so; the requested factual check remains unknown rather than passed.
- Bind the checked claim to the actual delivered answer. Parsing a transcript,
  comparing structured claims, and ensuring the spoken answer expresses them
  accurately need their own versioned checks. An ungrounded model judge is not
  an independent oracle.
- Version the scenario, extraction logic, checker, and application contract.
  Include allowlisted provider/model/prompt/tool configuration fingerprints in
  release identity when those changes affect behavior. Do not hash secrets or
  customer material as a substitute for redaction.
- Keep synthetic fixtures separate from production evidence. Store bounded
  verdicts and safe explanations in ECG; do not put transcripts, audio, prompts,
  customer details, or raw runtime configuration in evidence labels.

Automatic claim extraction, speech-to-answer verification, provider discovery,
whole-conversation correctness, and streaming/async workflow assembly are not
implemented. The existing linear todo trace adapter cannot establish them.
A deployment gate must explicitly require the relevant semantic scenarios;
observed traffic alone cannot reveal all missing critical behavior.

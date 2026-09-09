# Responses continuation replay qualification

M2g retained a 47,338-byte native OpenCode continuation rejected with `unexpected Responses reasoning item`. Its body SHA-256 is `27f1e6394827f78c79fc6fdefdaa423069bd6d47fe69f10bdbad7e1a46a64d8e`. The private artifact remains with the failed run and must not be imported into public source fixtures.

The request contains encrypted reasoning replay, an assistant message phase, and a completed function call/output pair. Provider-returned reasoning state is different from selecting a new reasoning effort: the route can permit replay through its existing model capability while keeping the role's reasoning controls unselected. No payload is rewritten or stripped.

Assistant message phase is a finite protocol field: absent, null, `commentary`, or `final_answer`. Other roles and typed item kinds must not gain acceptance of phase. Other unknown fields remain rejected.

Primary references checked during qualification:

- https://raw.githubusercontent.com/openai/openai-python/main/src/openai/types/responses/easy_input_message_param.py
- https://raw.githubusercontent.com/openai/openai-python/main/src/openai/types/responses/response_reasoning_item_param.py

The official assistant-input schema documents preserving phase on replay. The reasoning schema documents replaying completed encrypted content. These references do not turn a captured Muse/OpenCode request into proof of every provider's accepted dialect; the exact bounded artifact and route remain the qualification target.

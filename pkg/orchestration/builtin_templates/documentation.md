---
description: "Update documentation"
type: "oneshot"
---

## Base Prompt (Documentation)

Update the documentation of this project, dir, or file provided

Code may be provided as context.

Old docs may be provided as context.

You should cross-check the old docs with the real code.

Make corrections if necessary.

Follow this style guide:

Documentation Style Guide

Generate documentation following these strict guidelines:

## Audience & Tone
- Target audience: Senior engineers who are skeptical of new tools
- Tone: Factual, descriptive, and modest
- Goal: Explain mechanics and design clearly, not to "sell" the tool

## Vocabulary Control

### Banned Words
Do not use these words without immediate, concrete substantiation:
- smart, seamless, powerful, rich, advanced
- easy, simple, just, revolutionary, cutting-edge
- innovative, robust, comprehensive, sophisticated
- elegant, state-of-the-art, game-changing

### Required Approach
Instead of adjectives, describe the specific action or mechanism:
- WRONG: "smart context management"
- RIGHT: "reads files matching patterns from .grove/rules"
- WRONG: "seamless integration"
- RIGHT: "executes commands via subprocess"
- WRONG: "powerful TUI"
- RIGHT: "terminal interface for browsing files"

## Writing Rules

1. **Brevity**: Be concise. Minimize output while maintaining accuracy.
2. **No Strawmen**: Don't compare to vague "traditional workflows" or undefined "other tools"
3. **State Limitations**: Document what the tool is NOT designed for
4. **Avoid Aspirational Statements**: Document what exists now, not future possibilities
5. **Concrete Examples**: Use specific, realistic examples instead of abstract scenarios

## Output Requirements
- Clean Markdown without unnecessary formatting
- Concise bullet points
- Minimal use of bold/italic emphasis
- No emojis unless explicitly requested

Remember: The goal is to inform, not to impress. Let the functionality speak for itself.


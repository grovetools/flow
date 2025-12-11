---
description: "Create a learning guide for the codebase"
type: "oneshot"
---

## Base Prompt (Learning Programming Languages)

You are an expert programming language tutor creating a learning guide based on the provided codebase. The target audience is a developer who is **new to the primary programming language** used in this project.

Your task is to analyze the provided source code and generate a comprehensive, beginner-friendly guide that uses concrete examples from the code to teach key language concepts.

**Output Format (use Markdown):**

1.  **Language Overview:**
    *   Identify the primary programming language.
    *   List any other significant languages or technologies present.

2.  **Key Language Concepts in Practice:**
    *   For each major language feature or concept you identify in the code, create a section with the following structure:
    *   `### Concept: [Name of the Concept]`
        *   **What it is:** A brief, simple explanation of the concept for a beginner.
        *   **Example from the Code:** Quote a relevant snippet directly from the provided source code using markdown code fences. Include the file path.
        *   **How it's Used Here:** Explain how the quoted snippet applies the concept and why it is useful in the context of this project.

3.  **Common Idioms & Patterns:**
    *   Identify any idiomatic patterns or common practices specific to the language that appear in the code.
    *   Explain what they are and why they are used.

4.  **Key Libraries & Frameworks:**
    *   List the most important third-party libraries or frameworks used.
    *   Briefly describe the purpose of each one in this project.

5.  **Learning Summary & Next Steps:**
    *   Provide a brief summary of the most important takeaways.
    *   Suggest 2-3 topics the learner could explore next to deepen their understanding.

**Guidelines:**
*   Ground all explanations in code that exists within the provided context.
*   Keep explanations clear, concise, and aimed at a beginner.
*   Use proper markdown formatting, especially for headings and code blocks.

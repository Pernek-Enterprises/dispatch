import { loadConfig } from './state.js';

/**
 * Send a prompt to a model endpoint (direct API call).
 * Used for quick LLM jobs: triage, parse, answer.
 */
export async function callModel(modelId, prompt, { systemPrompt, maxTokens } = {}) {
  const models = loadConfig('models');
  const model = models[modelId];
  if (!model) throw new Error(`Unknown model: ${modelId}`);

  const messages = [];
  if (systemPrompt) {
    messages.push({ role: 'system', content: systemPrompt });
  }
  messages.push({ role: 'user', content: prompt });

  const response = await fetch(`${model.endpoint}/chat/completions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      messages,
      max_tokens: maxTokens || 2048,
      temperature: 0.7
    })
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(`Model ${modelId} error ${response.status}: ${text}`);
  }

  const data = await response.json();
  return data.choices?.[0]?.message?.content || '';
}

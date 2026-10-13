import { useState } from 'react'
import type { ChannelOptions } from './api'

function JSONOption({
  label,
  initial,
  change,
  hint,
}: {
  label: string
  initial: Record<string, unknown>
  change: (value: Record<string, unknown>) => void
  hint: string
}) {
  const [text, setText] = useState(JSON.stringify(initial, null, 2))
  return (
    <label>
      {label}
      <textarea
        rows={4}
        spellCheck={false}
        value={text}
        onInvalid={(event) => {
          const panel = event.currentTarget.closest('details')
          if (panel) panel.open = true
        }}
        onChange={(event) => {
          setText(event.target.value)
          try {
            const value: unknown = JSON.parse(event.target.value || '{}')
            if (!value || Array.isArray(value) || typeof value !== 'object')
              throw new Error('Enter a JSON object.')
            event.target.setCustomValidity('')
            change(value as Record<string, unknown>)
          } catch {
            event.target.setCustomValidity(
              'Enter a valid JSON object, for example {}.',
            )
          }
        }}
      />
      <small className="c-muted">{hint}</small>
    </label>
  )
}

export function ChannelCompatibility({
  value,
  change,
}: {
  value: ChannelOptions
  change: (value: ChannelOptions) => void
}) {
  const set = <K extends keyof ChannelOptions>(
    key: K,
    next: ChannelOptions[K],
  ) => change({ ...value, [key]: next })
  return (
    <details className="c-compatibility">
      <summary>Advanced · LLM compatibility & connection settings</summary>
      <p className="c-muted">
        Tune how this channel receives LLM requests. Defaults preserve existing
        connections. Changes apply after saving the channel.
      </p>
      <div className="c-form-grid">
        <label>
          Approved shared vector stores
          <textarea
            rows={2}
            defaultValue={(value.file_search_stores ?? []).join('\n')}
            placeholder="vs_… · one per line"
            onChange={(e) =>
              set(
                'file_search_stores',
                e.target.value.split(/[\s,]+/).filter(Boolean),
              )
            }
          />
          <small className="c-muted">
            Only list stores whose contents may be shared with every account
            using this channel. File search also requires a configured model
            tool price.
          </small>
        </label>
        <label>
          Compatibility dialect
          <select
            value={value.dialect || 'generic'}
            onChange={(e) => set('dialect', e.target.value)}
          >
            <option value="generic">Generic compatible service</option>
            <option value="openai">OpenAI</option>
            <option value="openrouter">OpenRouter</option>
            <option value="deepseek">DeepSeek</option>
            <option value="qwen">Qwen</option>
            <option value="vllm">vLLM</option>
            <option value="sglang">SGLang</option>
          </select>
        </label>
        <label>
          Thinking budget ratio
          <input
            type="number"
            min={0.1}
            max={0.95}
            step={0.01}
            placeholder="Provider default"
            value={value.thinking_budget_ratio || ''}
            onChange={(e) =>
              set('thinking_budget_ratio', Number(e.target.value))
            }
          />
          <small className="c-muted">
            Default: Claude 0.8, Gemini 0.6. The requested output limit stays
            authoritative.
          </small>
        </label>
        <label>
          HTTP protocol
          <select
            value={value.http_protocol || 'auto'}
            onChange={(e) => set('http_protocol', e.target.value)}
          >
            <option value="auto">Automatic · HTTP/2 when available</option>
            <option value="http1">HTTP/1.1</option>
          </select>
        </label>
        <label>
          HTTP/2 connection pools
          <input
            type="number"
            min={1}
            max={8}
            value={value.http2_shards || 1}
            onChange={(e) => set('http2_shards', Number(e.target.value))}
          />
          <small className="c-muted">
            Separate pools can spread traffic across upstream load balancers.
          </small>
        </label>
        <label>
          Request timeout · seconds
          <input
            type="number"
            min={30}
            max={3600}
            value={value.request_timeout_seconds || 600}
            onChange={(e) =>
              set('request_timeout_seconds', Number(e.target.value))
            }
          />
        </label>
        <label>
          Stream idle timeout · seconds
          <input
            type="number"
            min={15}
            max={600}
            value={value.stream_idle_seconds || 120}
            onChange={(e) => set('stream_idle_seconds', Number(e.target.value))}
          />
        </label>
        <label>
          Weight at equal priority
          <input
            type="number"
            min={1}
            max={10000}
            value={value.weight || 1}
            onChange={(e) => set('weight', Number(e.target.value))}
          />
        </label>
      </div>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.allow_stored_responses}
          onChange={(e) => set('allow_stored_responses', e.target.checked)}
        />
        Responses storage · allow account-scoped previous_response_id (7 days)
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.supports_compact}
          onChange={(e) => set('supports_compact', e.target.checked)}
        />
        Responses compact · this upstream supports /responses/compact
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.supports_alpha_search}
          onChange={(e) => set('supports_alpha_search', e.target.checked)}
        />
        Alpha search · this upstream supports /alpha/search and explicit tool
        pricing
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.preserve_model_suffix}
          onChange={(e) => set('preserve_model_suffix', e.target.checked)}
        />
        Preserve model suffix · treat the full upstream name literally
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.gemini_thought_signature}
          onChange={(e) => set('gemini_thought_signature', e.target.checked)}
        />
        Gemini tool history · enable New API's missing signature compatibility
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.cache_affinity}
          onChange={(e) => set('cache_affinity', e.target.checked)}
        />
        Prefer a stable channel for matching prompt prefixes within the same
        account
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.allow_service_tier}
          onChange={(e) => set('allow_service_tier', e.target.checked)}
        />
        Forward service_tier · confirm this model's pricing covers the selected
        upstream service level
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.omit_stream_options}
          onChange={(e) => set('omit_stream_options', e.target.checked)}
        />
        Omit stream_options for providers that reject it · streams without
        confirmed usage remain uncharged
      </label>
      <label className="c-checkbox">
        <input
          type="checkbox"
          checked={!!value.thinking_to_content}
          onChange={(e) => set('thinking_to_content', e.target.checked)}
        />
        Display returned reasoning in Chat content with think tags
      </label>
      <div className="c-form-grid">
        <JSONOption
          label="Additional upstream headers · JSON"
          initial={value.headers || {}}
          change={(v) => set('headers', v as Record<string, string>)}
          hint="Allowed: Anthropic-Version, Anthropic-Beta, OpenAI-Organization, OpenAI-Project, HTTP-Referer, X-Title. Use the API key field for credentials."
        />
        <JSONOption
          label="Parameter overrides · JSON"
          initial={value.parameter_overrides || {}}
          change={(v) => set('parameter_overrides', v)}
          hint={
            'Example: {"temperature":0.7}. Overrides run before model modifiers. Routing, content, output limits and billing fields cannot be overridden.'
          }
        />
        <label>
          Remove parameters
          <input
            value={(value.drop_parameters || []).join(', ')}
            onChange={(e) =>
              set(
                'drop_parameters',
                e.target.value
                  .split(',')
                  .map((v) => v.trim())
                  .filter(Boolean),
              )
            }
            placeholder="temperature, top_p"
          />
          <small className="c-muted">
            Comma-separated LLM controls. Removal follows overrides; explicit
            model modifiers are then applied.
          </small>
        </label>
      </div>
    </details>
  )
}

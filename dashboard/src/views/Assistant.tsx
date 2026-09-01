import { useCallback, useEffect, useRef, useState } from "react";
import {
  api,
  askAssistant,
  type AssistantMessage,
  type AssistantModel,
  type AssistantStatus,
} from "../api";
import { describeError } from "../App";
import { Message } from "../components/Message";
import { Markdown } from "../components/Markdown";

/**
 * Asking this machine a question.
 *
 * The model runs here. It is a file on this disk answered by a process on
 * loopback, with no account, no upstream and nothing leaving the network — and
 * that is the only reason a 2.6 GiB model on a nine-year-old laptop is worth
 * having instead of something far better over the internet. So the screen says
 * it, once, where it will be read, rather than in documentation.
 *
 * **It is slow, and the design admits that.** Measured decode on this hardware
 * is about six and a half tokens a second: a paragraph takes half a minute and
 * a full answer over two. Every choice here follows from that number — the
 * answer streams so there is motion from the first second, thinking is off by
 * default because it costs four times the tokens before a word appears, there
 * is always a way to stop, and the machine takes one question at a time and
 * says so rather than queueing somebody behind a two-minute wait.
 *
 * **The transcript lives in this tab and nowhere else.** It is sent back with
 * each turn and kept by nobody. A permanent record of everything anybody asked
 * their house server would be a new and fairly personal database, and this
 * feature does not need one.
 */

interface Turn extends AssistantMessage {
  /** Set while this answer is still being written. */
  streaming?: boolean;
  /** Set when the answer stopped because it ran out of room. */
  truncated?: boolean;
}

export function Assistant() {
  const [status, setStatus] = useState<AssistantStatus | null>(null);
  const [turns, setTurns] = useState<Turn[]>([]);
  const [draft, setDraft] = useState("");
  const [think, setThink] = useState(false);
  // Which model answers. Empty means the primary one; the server treats a
  // request naming no model as the safe one, so this never defaults to the
  // unrestricted one by accident.
  const [model, setModel] = useState("");
  const [error, setError] = useState<ReturnType<typeof describeError> | null>(null);
  const [asking, setAsking] = useState(false);

  // Returned by askAssistant. Calling it aborts the request, which is what
  // gives the server's single slot back.
  const stopRef = useRef<(() => void) | null>(null);
  const transcriptRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    let cancelled = false;
    api
      .assistant()
      .then((value) => {
        if (!cancelled) setStatus(value);
      })
      .catch((caught) => {
        if (!cancelled) setError(describeError(caught));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // Stop the answer if this screen goes away. Without it, navigating to another
  // tab mid-answer leaves the model working on something nobody will read and
  // the slot held against the next question.
  useEffect(() => () => stopRef.current?.(), []);

  // Follow the answer as it is written, which is most of what makes a slow
  // model bearable to watch.
  useEffect(() => {
    const element = transcriptRef.current;
    if (element) element.scrollTop = element.scrollHeight;
  }, [turns]);

  const ask = useCallback(() => {
    const question = draft.trim();
    if (!question || asking) return;

    setError(null);
    setDraft("");
    setAsking(true);

    const history: AssistantMessage[] = [
      ...turns.map(({ role, content }) => ({ role, content })),
      { role: "user", content: question },
    ];

    // The question and the empty answer it is about to fill go in together, so
    // there is somewhere for the first token to land.
    setTurns((previous) => [
      ...previous,
      { role: "user", content: question },
      { role: "assistant", content: "", streaming: true },
    ]);

    const replaceLast = (update: (turn: Turn) => Turn) =>
      setTurns((previous) => {
        const last = previous[previous.length - 1];
        // "Start again" can empty the transcript while an answer is still
        // arriving. Nothing to update is not an error; it means the answer
        // being written is one nobody is waiting for any more.
        if (!last) return previous;
        const next = [...previous];
        next[next.length - 1] = update(last);
        return next;
      });

    stopRef.current = askAssistant(
      history,
      {
        onToken: (text) =>
          replaceLast((turn) => ({ ...turn, content: turn.content + text })),
        onDone: (reason) => {
          replaceLast((turn) => ({
            ...turn,
            streaming: false,
            truncated: reason === "length",
          }));
          setAsking(false);
          stopRef.current = null;
        },
        onError: (caught) => {
          setError(describeError(caught));
          // Drop the empty answer rather than leave a blank bubble where an
          // answer was going to be.
          setTurns((previous) => {
            const last = previous[previous.length - 1];
            return last && last.role === "assistant" && last.content === ""
              ? previous.slice(0, -1)
              : previous;
          });
          setAsking(false);
          stopRef.current = null;
        },
      },
      model ? { think, model } : { think },
    );
  }, [draft, asking, turns, think, model]);

  const stop = useCallback(() => {
    stopRef.current?.();
    stopRef.current = null;
    setAsking(false);
    setTurns((previous) => {
      const next = [...previous];
      const last = next[next.length - 1];
      if (last && last.streaming) {
        next[next.length - 1] = { ...last, streaming: false };
      }
      return next;
    });
  }, []);

  const startAgain = useCallback(() => {
    stopRef.current?.();
    stopRef.current = null;
    setTurns([]);
    setError(null);
    setAsking(false);
  }, []);

  if (status && !status.available) {
    return (
      <section className="card">
        <h2>Assistant</h2>
        <Message
          tone="info"
          title="This server has no assistant."
          detail={status.reason}
          technical="Set --assistant-url on core to point it at a local model."
        />
      </section>
    );
  }

  // Offer order comes from the server. The first non-unrestricted model is the
  // one a request naming nothing reaches, so it is what an empty selection
  // means here too.
  const choices: AssistantModel[] = status?.models ?? [];
  const selected =
    choices.find((choice) => choice.id === model) ??
    choices.find((choice) => !choice.unrestricted);

  const nearLimit = status !== null && turns.length >= status.max_turns - 4;
  const full = status !== null && turns.length >= status.max_turns;

  return (
    <section className="card assistant">
      <div className="row row-between assistant-head">
        <h2>Assistant</h2>
        {turns.length > 0 && (
          <button type="button" className="quiet" onClick={startAgain}>
            Start again
          </button>
        )}
      </div>

      {error && <Message tone="error" {...error} />}

      <div className="assistant-transcript" ref={transcriptRef} aria-live="polite">
        {turns.length === 0 ? (
          <p className="muted assistant-empty">
            Ask this server something. It answers using a model that runs on this
            machine — nothing you type here leaves your network.
          </p>
        ) : (
          turns.map((turn, index) => (
            <div
              key={index}
              className={
                turn.role === "user" ? "assistant-turn assistant-you" : "assistant-turn"
              }
            >
              <span className="assistant-who">
                {turn.role === "user" ? "You" : "Assistant"}
              </span>
              <div className="assistant-text">
                {turn.role === "user" ? (
                  // Shown exactly as typed. A question is not a document, and
                  // rendering somebody's own asterisks back at them as bold is
                  // both wrong and confusing.
                  <p>{turn.content}</p>
                ) : (
                  <Markdown text={turn.content} />
                )}
                {turn.streaming && <span className="assistant-cursor" aria-hidden="true" />}
              </div>
              {turn.truncated && (
                <p className="hint hint-warning">
                  This answer reached its length limit and stopped here.
                </p>
              )}
            </div>
          ))
        )}
      </div>

      <form
        className="assistant-ask"
        onSubmit={(event) => {
          event.preventDefault();
          ask();
        }}
      >
        <textarea
          className="assistant-input"
          value={draft}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={(event) => {
            // Enter sends, shift+Enter is a new line — what every other message
            // box does, and what people will try first.
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault();
              ask();
            }
          }}
          placeholder={full ? "Start again to keep asking." : "Ask something…"}
          rows={2}
          disabled={asking || full}
          maxLength={status?.max_chars}
          aria-label="Your question"
        />
        {asking ? (
          <button type="button" className="quiet" onClick={stop}>
            Stop
          </button>
        ) : (
          <button type="submit" className="primary" disabled={!draft.trim() || full}>
            Ask
          </button>
        )}
      </form>

      {selected?.unrestricted && (
        <p className="assistant-unrestricted" role="status">
          <strong>Unrestricted model.</strong> Its refusals were removed by
          someone other than the people who trained it, so it will answer things
          the usual one declines, and it is less reliable exactly there. It runs
          with no network and cannot see your files. Homebase did not start it
          and cannot stop it.
        </p>
      )}

      <div className="row row-between assistant-footer">
        {choices.length > 1 && (
          <label className="assistant-model-choice">
            Model{" "}
            <select
              value={model}
              onChange={(event) => setModel(event.target.value)}
              disabled={asking}
            >
              {choices.map((choice) => (
                <option
                  key={choice.id}
                  value={choice.id}
                  disabled={!choice.available}
                >
                  {choice.label}
                  {choice.available ? "" : " — not running"}
                </option>
              ))}
            </select>
          </label>
        )}
        <label className="assistant-think">
          <input
            type="checkbox"
            checked={think}
            onChange={(event) => setThink(event.target.checked)}
            disabled={asking}
          />{" "}
          Think first
          <span className="muted"> — better on hard questions, several times slower</span>
        </label>
        {status?.model && <span className="muted assistant-model">{status.model}</span>}
      </div>

      {full ? (
        <p className="hint hint-warning">
          This conversation is as long as the assistant can hold. Start again to
          keep asking.
        </p>
      ) : nearLimit ? (
        <p className="hint">
          This conversation is getting long. The assistant holds{" "}
          {status?.max_turns} messages.
        </p>
      ) : null}
    </section>
  );
}

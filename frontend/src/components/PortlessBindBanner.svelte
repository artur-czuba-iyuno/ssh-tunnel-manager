<script lang="ts">
  import { onMount } from "svelte";
  import { EventsOn, EventsOff } from "../../wailsjs/runtime/runtime";
  import { AuthorizePrivilegedBind, CopyToClipboard, GetPortlessFallback, RestartApp } from "../../wailsjs/go/main/App";
  import { showToast } from "../stores/toast";

  interface BindError {
    tunnelId: string;
    domain: string;
    port: number;
    message: string;
    needsCapability: boolean;
    setcapCommand?: string;
  }

  interface DNSFallback {
    tunnelId: string;
    message: string;
    recommendation?: string;
    command?: string;
  }

  let err: BindError | null = null;
  let fallback: DNSFallback | null = null;
  let authorizing = false;
  // Set after a successful grant: the capability only applies at exec time, so
  // the app must restart before the privileged bind will work.
  let granted = false;

  onMount(() => {
    EventsOn("portless:bind-failed", (e: BindError) => {
      err = e;
      fallback = null;
      granted = false;
    });
    EventsOn("portless:dns-unavailable", (e: DNSFallback) => {
      fallback = e;
      err = null;
    });
    EventsOn("portless:dns-restored", () => {
      fallback = null;
    });
    GetPortlessFallback()
      .then((current) => {
        if (current) fallback = current;
      })
      .catch(() => {
        /* the live event still covers conflicts discovered after mount */
      });
    return () => {
      EventsOff("portless:bind-failed");
      EventsOff("portless:dns-unavailable");
      EventsOff("portless:dns-restored");
    };
  });

  async function authorize() {
    if (!err) return;
    authorizing = true;
    try {
      await AuthorizePrivilegedBind();
      granted = true;
    } catch (e: any) {
      showToast(`Authorization failed: ${e}`, "error", 4000);
    } finally {
      authorizing = false;
    }
  }

  async function restart() {
    try {
      await RestartApp();
    } catch (e: any) {
      showToast(`Restart failed: ${e}`, "error", 4000);
    }
  }

  async function copyCommand() {
    if (!err?.setcapCommand) return;
    try {
      await CopyToClipboard(err.setcapCommand);
      showToast("Command copied", "success");
    } catch {
      /* clipboard errors are non-fatal */
    }
  }

  async function copyFallbackCommand() {
    if (!fallback?.command) return;
    try {
      await CopyToClipboard(fallback.command);
      showToast("Command copied", "success");
    } catch {
      /* clipboard errors are non-fatal */
    }
  }
</script>

{#if fallback}
  <div class="bind-banner warning" role="status">
    <div class="bind-body">
      <span class="bind-tag warning">[ PORTLESS FALLBACK ]</span>
      <span class="bind-msg">{fallback.message}</span>
    </div>
    {#if fallback.recommendation}
      <span class="bind-msg">{fallback.recommendation}</span>
    {/if}
    {#if fallback.command}
      <code class="bind-cmd">{fallback.command}</code>
    {/if}
    <div class="bind-actions">
      {#if fallback.command}
        <button class="bind-btn" on:click={copyFallbackCommand}>[ COPY CMD ]</button>
      {/if}
      <button class="bind-btn" on:click={() => (fallback = null)}>[ DISMISS ]</button>
    </div>
  </div>
{:else if err}
  <div class="bind-banner" role="alert">
    {#if granted}
      <div class="bind-body">
        <span class="bind-tag ok">[ PORTLESS ]</span>
        <span class="bind-msg">Permission granted. Linux applies it on next launch — restart to enable the privileged-port forward.</span>
      </div>
      <div class="bind-actions">
        <button class="bind-btn primary" on:click={restart}>[ RESTART NOW ]</button>
        <button class="bind-btn" on:click={() => (err = null)}>[ LATER ]</button>
      </div>
    {:else}
      <div class="bind-body">
        <span class="bind-tag">[ PORTLESS ]</span>
        <span class="bind-msg">{err.message}</span>
      </div>
      {#if err.needsCapability && err.setcapCommand}
        <code class="bind-cmd">{err.setcapCommand}</code>
      {/if}
      <div class="bind-actions">
        {#if err.needsCapability}
          <button class="bind-btn primary" on:click={authorize} disabled={authorizing}>
            {authorizing ? "// authorizing..." : "[ AUTHORIZE ]"}
          </button>
          {#if err.setcapCommand}
            <button class="bind-btn" on:click={copyCommand}>[ COPY CMD ]</button>
          {/if}
        {/if}
        <button class="bind-btn" on:click={() => (err = null)}>[ DISMISS ]</button>
      </div>
    {/if}
  </div>
{/if}

<style>
  .bind-banner {
    position: fixed;
    bottom: 16px;
    left: 16px;
    right: 16px;
    z-index: 9998;
    display: flex;
    flex-direction: column;
    gap: 8px;
    background: #0a0a0a;
    border: 1px solid #ff4444;
    border-radius: 2px;
    padding: 10px 14px;
    box-shadow: 0 2px 16px rgba(255, 68, 68, 0.25);
    font-family: 'JetBrains Mono', monospace;
    font-size: 11px;
    color: var(--text);
    animation: slide-up 0.18s ease-out;
  }

  .bind-body {
    display: flex;
    align-items: baseline;
    gap: 8px;
  }

  .bind-tag {
    color: #ff4444;
    font-weight: 600;
    white-space: nowrap;
  }

  .bind-tag.ok {
    color: var(--accent);
  }

  .bind-banner.warning {
    border-color: #f2c94c;
    box-shadow: 0 2px 16px rgba(242, 201, 76, 0.2);
  }

  .bind-tag.warning {
    color: #f2c94c;
  }

  .bind-msg {
    line-height: 1.4;
  }

  .bind-cmd {
    display: block;
    background: #141414;
    border: 1px solid var(--border);
    border-radius: 2px;
    padding: 6px 8px;
    color: var(--accent);
    user-select: all;
    overflow-x: auto;
    white-space: nowrap;
  }

  .bind-actions {
    display: flex;
    gap: 8px;
    justify-content: flex-end;
  }

  .bind-btn {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--text);
    font-family: inherit;
    font-size: 11px;
    padding: 4px 10px;
    border-radius: 2px;
    cursor: pointer;
  }

  .bind-btn:hover:not(:disabled) {
    border-color: var(--text);
  }

  .bind-btn.primary {
    border-color: var(--accent);
    color: var(--accent);
  }

  .bind-btn:disabled {
    opacity: 0.5;
    cursor: default;
  }

  @keyframes slide-up {
    from {
      opacity: 0;
      transform: translateY(8px);
    }
    to {
      opacity: 1;
      transform: translateY(0);
    }
  }
</style>

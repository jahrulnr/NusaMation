/* NusaMation web control center — vanilla JS modules, no build step. */
import { bindEvents, syncAppHeight } from "./components/events.js";
import { initSelects } from "./components/selects.js";
import { boot } from "./components/boot.js";

initSelects();
bindEvents();
syncAppHeight();
boot();

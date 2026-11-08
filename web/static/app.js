/* NusaMation web control center — vanilla JS modules, no build step. */
import { bindEvents, syncAppHeight } from "./components/events.js";
import { boot } from "./components/boot.js";

bindEvents();
syncAppHeight();
boot();

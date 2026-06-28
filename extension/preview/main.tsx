import { createRoot } from "react-dom/client";
import OptionsApp from "@/entrypoints/options/App";
import PopupApp from "@/entrypoints/popup/App";
import "./style.css";

const popup = document.getElementById("popup");
const dash = document.getElementById("dash");
if (popup) createRoot(popup).render(<PopupApp />);
if (dash) createRoot(dash).render(<OptionsApp />);

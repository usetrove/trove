# Privacy law notes (for lawyer review)

**Not legal advice, and no legal conclusions are drawn.** This page lists what the official texts say, quoted
exactly, and what that raises for the two kinds of data Trove could handle beyond the user's own computer: the
opt-in summary that exists today, and a team edition that does not exist yet. The open questions are for a lawyer.

Fetched 2026-11-06. Every quote is at most 125 characters and is from one fetch; the source page is the authority.
Anything not retrieved or not read word for word is marked.

## The two data sets

1. **The opt-in summary** (exists; off by default). A random 128-bit install id, version, OS and CPU type, the length of
   a period in days, a coarse request-count bucket, which agents were used (fixed list), a rounded waste percentage,
   per-optimizer name and effectiveness, a guardrail count. No prompts, code, paths, hashes, model names, dollar
   amounts, exact token counts or exact dates. Sent at most weekly after `trove contribute enable`. The receiver keeps
   the UTC day and no IP address, keeps no request logs, and deletes rows after 365 days or on request by install id.
   It runs on Cloudflare, which may keep its own operational records. Full field list: [data.md](data.md).
2. **Team data** (not built). A proposed design only: a pseudonymous member id, daily counts and estimated cost, model and
   optimizer names, an admin-chosen group label; aggregated by default with a minimum group size. See "Team edition" in
   [data.md](data.md). Nothing is collected today.

Trove itself keeps the rest of its data on the user's computer and sends none of it. Whether that is "collection" by
Trove at all is a question below.

## Sources and what was read

| Source | Date on the page | Read |
|---|---|---|
| [OPC: PIPEDA fair information principles](https://www.priv.gc.ca/en/privacy-topics/privacy-laws-in-canada/the-personal-information-protection-and-electronic-documents-act-pipeda/p_principle/) | Date modified 2025-05-29 | In full |
| [PIPEDA (S.C. 2000, c. 5)](https://laws-lois.justice.gc.ca/eng/acts/P-8.6/FullText.html) | "Act current to 2026-10-19 and last amended on 2025-03-04." | **Partly**: about the first 40,000 of 134,000 characters verbatim (through s. 9(2.3)); the rest, and the Schedule 1 clauses, not verified |
| [BC Personal Information Protection Act (SBC 2003, c 63)](https://www.bclaws.gov.bc.ca/civix/document/id/complete/statreg/03063_01) | "This Act is current to November 3, 2026" | **Partly**: about 40,000 of 76,000 characters verbatim (start through s. 23(3)(a)), which includes ss. 1, 8, 10 and 13 |
| [GDPR, Regulation (EU) 2016/679](https://eur-lex.europa.eu/eli/reg/2016/679/oj/eng) | "of 27 April 2016"; "Current consolidated version: 04/05/2016" | **Partly**: recitals 1 to 23 and 129 to 173 and Arts. 1 to 2 verbatim. **Arts. 4, 5, 6, 17, 20 and 88 and Recital 26 were not read word for word** |
| [California AG: CCPA page](https://oag.ca.gov/privacy/ccpa) | "Updated on August 28, 2026" | **Partly** (about 40,000 of 54,000 characters). The statute text on leginfo.legislature.ca.gov returned HTTP 403: **NOT RETRIEVED** |
| [New York Civil Rights Law § 52-c*2](https://www.nysenate.gov/legislation/laws/CVR/52-C*2) (electronic monitoring; the page notes there are two section 52-c's) | "most recent revision (from 2022-05-13)" | In full |
| Delaware Code Title 19 [§ 705](https://delcode.delaware.gov/title19/c007/sc01/index.html) | none on the page | In full |
| Connecticut General Statutes § 31-48d | | **NOT RETRIEVED** (TLS certificate error) |
| Ontario Employment Standards Act, 2000, Part XI.1 | | **NOT RETRIEVED** (the page returned no statute text) |

## Canada: PIPEDA

What the text says (verbatim):

- s. 2(1): "personal information means information about an identifiable individual."
- s. 4(1)(a): "the organization collects, uses or discloses in the course of commercial activities"
- s. 4(1)(b), employees: "is about an employee of, or an applicant for employment with, the organization and that the organization collects," ... "in connection with the operation of a federal work, undertaking or business."
- s. 5(3): "An organization may collect, use or disclose personal information only for purposes that a reasonable person"
- OPC principle 3, consent: "The knowledge and consent of the individual are required for the collection, use, or disclosure of personal information,"
- OPC principle 4, limiting collection: "The collection of personal information must be limited to that which is needed for the purposes identified"

Raised by Trove's data: the opt-in summary is built around a random id and coarse figures, designed so that it is not
"about an identifiable individual", but the id lets one install's weekly summaries be linked to each other and the receiver
can delete by that id. Consent for the summary is explicit and recorded. For a team edition, PIPEDA's employee provision
in s. 4(1)(b) as quoted reaches employees only of federal works, undertakings or businesses; other private-sector
employers in Canada fall under provincial law (below) or, in Quebec, Alberta and BC, provincial private-sector statutes.

Open questions: (1) Is a random install id, with coarse figures, "information about an identifiable individual"? (2) Is
data that stays on the user's computer collected by Trove's developer at all? (3) Is the consent screen (exact text and
address, then yes) sufficient meaningful consent, and does the receiver's use for checking savings claims match the stated
purpose? (4) Does the developer need a privacy officer or policy for the receiver?

## British Columbia: PIPA

What the text says (verbatim):

- s. 1: ""personal information" means information about an identifiable individual and includes employee personal information"
- s. 1: ""employee personal information" means personal information about an individual that is collected, used or disclosed solely" (the definition continues beyond the quoted words)
- s. 8(1): "An individual is deemed to consent to the collection, use or disclosure of personal information by an organization"
- s. 10(1): "On or before collecting personal information about an individual from the individual, an organization must disclose" ... "the purposes for the collection of the information"
- s. 13(1): "an organization may collect employee personal information without the consent of the individual."
- s. 13(2)(b): "the collection is reasonable for the purposes of establishing, managing or terminating an employment relationship"
- s. 13(3): "An organization must notify an individual that it will be collecting employee personal information about the individual" ... "before the organization collects the employee personal information without the consent of the individual."

Raised by Trove's data: the same identifiability question as above for the opt-in summary. For a team edition in BC, the
quoted s. 13 would allow collection of employee personal information without consent only where reasonable for the
employment relationship, and requires prior notice. Whether usage metadata about a person's coding tool qualifies as
"reasonable for managing the employment relationship" is not answered by the text.

Open questions: (5) Would per-person team metrics be "employee personal information" and reasonable under s. 13(2)(b), or
should team members consent? (6) What must the s. 13(3) notice contain? (7) Does a user's own employer's use of Trove on a
work computer change who the "organization" is?

## Ontario (not retrieved)

The Employment Standards Act, 2000 requires some employers to have a written policy on electronic monitoring. **This page
could not be fetched and nothing here is drawn from it.** A lawyer should confirm whether it applies to a team edition
and to employers who deploy Trove to employees (threshold, content of the policy, timing).

## European Union and United Kingdom: GDPR

Read verbatim: Recital 155: "Member State law or collective agreements, including 'works agreements', may provide for specific rules on the processing of" (the sentence continues in the recital).

**Not read word for word (the fetch returned a summary only): Article 4(1) (definition of personal data), Article 4(5)
(pseudonymisation), Recital 26, Article 5(1)(c) (data minimisation), Article 6(1) (lawful bases), Article 17 (erasure),
Article 20 (portability) and Article 88 (employment context).** The questions below name them because they are the
provisions a lawyer would start from, not because this page states what they say.

Raised by Trove's data: GDPR distinguishes identifiable from pseudonymous from anonymous information. The opt-in
summary's install id is pseudonymous at most; whether the summary is personal data depends on whether the receiver, or
anyone who can combine it with other data, could single out a person. Trove's local design (data stays on the computer;
`trove data export` and `trove data delete`) is aimed at access, portability and erasure, but those rights apply to a
controller, which for local-only data may be the user alone. For a team edition, Recital 155 as quoted points to national
law and works agreements for employee data.

Open questions: (8) Is the install id personal data under Art. 4(1) and Recital 26, and if so which lawful basis in Art. 6(1)
applies to the summary (consent is how it is built)? (9) Is the developer a controller, a processor or neither for the
opt-in receiver, and is a data protection impact assessment needed? (10) Transfers: the receiver runs on a US company's
network; what transfer mechanism applies to EU or UK users? (11) For a team edition, Art. 88 and national laws on employee
monitoring (several member states require works-council involvement or consent); what is required per country? (12) Does
a deletion request by install id satisfy Art. 17 for the receiver's rows?

## United States

California. The Attorney General's page, "Updated on August 28, 2026", says (verbatim):

- "Personal information is information that identifies, relates to, or could reasonably be linked with you or your household."
- "No. The exemptions for employment-related personal information and personal information reflecting business-to-business" ... "expired on December 31, 2022."

So employee data is no longer outside the CCPA as of that answer. The statutory text and its "unique identifier" and
"deidentified" definitions were **not retrieved**.

New York, Civil Rights Law § 52-c*2 (monitoring notice), verbatim: "shall give prior written notice upon hiring to all employees who are subject to electronic monitoring." and "and acknowledged by the employee either in writing or electronically." and "Each employer shall also post the notice of electronic monitoring in a conspicuous place". "employer means any individual, corporation, partnership, firm, or association with a place of business in the state." Enforcement: "The attorney general may enforce the provisions of this section."

Delaware, 19 Del. C. § 705, verbatim: "No employer, nor any agent or any representative of any employer, shall monitor or otherwise intercept" ... "Provides an electronic notice of such monitoring or intercepting policies or activities to the employee at least once" ... "Has first given a 1-time notice to the employee of such monitoring or intercepting activity or policies." Penalty: "a civil penalty of $100 for each such violation." The section is headed "Notice of monitoring of telephone transmissions, electronic mail and Internet usage"; whether coding-tool usage metadata is within it is for a lawyer.

Connecticut's electronic monitoring notice statute was not retrieved. Other states (for example those with comprehensive
consumer privacy laws) were not read at all.

Raised by Trove's data: a comprehensive state privacy law may treat a persistent identifier as personal information; an
employer deploying a team edition may owe employees written notice of electronic monitoring in some states.

Open questions: (13) Does any state treat the install id as personal information, and do thresholds or exemptions apply to
a small free tool? (14) For a team edition: which states require notice, acknowledgement or posting, and does collecting
only aggregate, content-free metadata count as "electronic monitoring"? (15) Do "sale" or "sharing" definitions reach the
opt-in summary (it is sent to a service the project operates, with no advertising use)?

## What the design does about it (not a legal conclusion)

- Nothing is sent unless the user turns it on after seeing the exact text and address; a damaged consent record means off.
- The summary holds coarse figures, no content and no dates finer than "the length of the period".
- The user can see (`trove contribute`), export (`trove data export`), delete locally (`trove data delete`), and request
  deletion at the receiver by install id. Deletion at the receiver is `trove contribute delete`, which the user runs on purpose;
  nothing else sends it (CLAUDE.md rule 1, part c).
- A team edition, if built, must default to aggregated views with a minimum group size, never hold content, make
  individual views opt-in by the individual, tell every member what is shared before it is shared, and be added to
  CLAUDE.md as a new network rule first.

## Questions to take to a lawyer

The numbered questions above, plus the provider-terms questions in [compliance.md](compliance.md). Two more:

16. Does publishing the benchmark lab's aggregate results (costs, pass rates, no content) raise any privacy issue? (It
    holds no personal data by design.)
17. Do we need a published privacy policy and a named contact beyond [PRIVACY.md](../PRIVACY.md) and [data.md](data.md)?

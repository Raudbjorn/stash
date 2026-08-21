import { CriterionModifier, StringCriterionInput } from "src/core/generated-graphql";
import { ModifierCriterionOption, StringCriterion } from "./criterion";

const transcodeBenefitOptions = ["HIGH", "MEDIUM", "LOW"];

export const TranscodeBenefitCriterionOption = new ModifierCriterionOption({
  messageID: "transcode_benefit",
  type: "transcode_benefit",
  modifierOptions: [
    CriterionModifier.Equals,
    CriterionModifier.NotEquals,
    CriterionModifier.IsNull,
    CriterionModifier.NotNull,
  ],
  defaultModifier: CriterionModifier.Equals,
  options: transcodeBenefitOptions,
  makeCriterion: () => new TranscodeBenefitCriterion(),
});

export class TranscodeBenefitCriterion extends StringCriterion {
  constructor() {
    super(TranscodeBenefitCriterionOption);
  }

  public toCriterionInput(): StringCriterionInput {
    return {
      value: this.value,
      modifier: this.modifier,
    };
  }
}
